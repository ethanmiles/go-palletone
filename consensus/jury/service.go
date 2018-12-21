/*
	This file is part of go-palletone.
	go-palletone is free software: you can redistribute it and/or modify
	it under the terms of the GNU General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.
	go-palletone is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU General Public License for more details.
	You should have received a copy of the GNU General Public License
	along with go-palletone.  If not, see <http://www.gnu.org/licenses/>.
*/

/*
 * @author PalletOne core developers <dev@pallet.one>
 * @date 2018
 */
package jury

import (
	"bytes"
	"fmt"
	"reflect"
	"sync"
	"time"

	"encoding/json"
	"github.com/dedis/kyber"
	"github.com/palletone/go-palletone/common"
	"github.com/palletone/go-palletone/common/event"
	"github.com/palletone/go-palletone/common/log"
	"github.com/palletone/go-palletone/common/p2p"
	"github.com/palletone/go-palletone/common/rlp"
	"github.com/palletone/go-palletone/contracts"
	"github.com/palletone/go-palletone/core/accounts/keystore"
	"github.com/palletone/go-palletone/core/gen"
	"github.com/palletone/go-palletone/dag"
	cm "github.com/palletone/go-palletone/dag/common"
	"github.com/palletone/go-palletone/dag/errors"
	"github.com/palletone/go-palletone/dag/modules"
	"github.com/palletone/go-palletone/dag/txspool"
	"github.com/palletone/go-palletone/tokenengine"
)

type PeerType = int

const (
	CONTRACT_SIG_NUM = 3

	TJury     = 2
	TMediator = 4
)

type Juror struct {
	name        string
	address     common.Address
	InitPartPub kyber.Point
}

//合约节点类型、地址信息
type nodeInfo struct {
	addr  common.Address
	ntype int //1:default, 2:jury, 4:mediator
}
type PalletOne interface {
	GetKeyStore() *keystore.KeyStore
	TxPool() txspool.ITxPool

	MockContractLocalSend(event ContractExeEvent)
	MockContractSigLocalSend(event ContractSigEvent)

	ContractBroadcast(event ContractExeEvent)
	ContractSigBroadcast(event ContractSigEvent)

	GetLocalMediators() []common.Address
	IsLocalActiveMediator(add common.Address) bool

	SignGenericTransaction(from common.Address, tx *modules.Transaction) (*modules.Transaction, error)
}

type iDag interface {
	GetTxFee(pay *modules.Transaction) (*modules.InvokeFees, error)
	GetAddrByOutPoint(outPoint *modules.OutPoint) (common.Address, error)
	GetActiveMediators() []common.Address
	IsActiveJury(add common.Address) bool
	IsActiveMediator(add common.Address) bool
	GetAddr1TokenUtxos(addr common.Address, asset *modules.Asset) (map[modules.OutPoint]*modules.Utxo, error)
	CreateGenericTransaction(from, to common.Address, daoAmount, daoFee uint64,
		msg *modules.Message) (*modules.Transaction, uint64, error)
}

type contractTx struct {
	state      int                    //contract run state, 0:default, 1:running
	list       []common.Address       //dynamic
	reqTx      *modules.Transaction   //request contract
	rstTx      *modules.Transaction   //contract run result---system
	sigTx      *modules.Transaction   //contract sig result---user, 0:local, 1,2 other
	rcvTx      []*modules.Transaction //todo 本地没有没有接收过请求合约，缓存已经签名合约
	tm         time.Time              //create time
	valid      bool                   //contract request valid identification
	executable bool                   //contract executable,sys on mediator, user on jury
}

type Processor struct {
	name     string
	ptype    PeerType
	ptn      PalletOne
	dag      iDag
	local    map[common.Address]*JuryAccount //[]common.Address //local account addr
	contract *contracts.Contract
	locker   *sync.Mutex
	quit     chan struct{}
	mtx      map[common.Hash]*contractTx

	contractExecFeed  event.Feed
	contractExecScope event.SubscriptionScope
	contractSigFeed   event.Feed
	contractSigScope  event.SubscriptionScope
	idag              dag.IDag
}

func NewContractProcessor(ptn PalletOne, dag iDag, contract *contracts.Contract, cfg *Config) (*Processor, error) {
	if ptn == nil || dag == nil {
		return nil, errors.New("NewContractProcessor, param is nil")
	}

	accounts := make(map[common.Address]*JuryAccount, 0)
	for _, cfg := range cfg.Accounts {
		account := cfg.configToAccount()
		addr := account.Address
		accounts[addr] = account
	}

	p := &Processor{
		name:     "conractProcessor",
		ptn:      ptn,
		dag:      dag,
		contract: contract,
		local:    accounts,
		locker:   new(sync.Mutex),
		quit:     make(chan struct{}),
		mtx:      make(map[common.Hash]*contractTx),
	}

	log.Info("NewContractProcessor ok", "local address:", p.local)
	//log.Info("NewContractProcessor", "info:", p.local)
	return p, nil
}

func (p *Processor) Start(server *p2p.Server) error {
	//启动消息接收处理线程
	//合约执行节点更新线程
	//合约定时清理线程
	go p.ContractTxDeleteLoop()
	return nil
}

func (p *Processor) Stop() error {
	close(p.quit)
	log.Debug("contract processor stop")
	return nil
}

func (p *Processor) SubscribeContractEvent(ch chan<- ContractExeEvent) event.Subscription {
	return p.contractExecScope.Track(p.contractExecFeed.Subscribe(ch))
}

func (p *Processor) isLocalActiveJury(add common.Address) bool {
	if _, ok := p.local[add];ok {
		return p.dag.IsActiveJury(add)
	}
	return false
}

func (p *Processor) ProcessContractEvent(event *ContractExeEvent) error {
	if event == nil {
		return errors.New("ProcessContractEvent param is nil")
	}
	reqId := event.Tx.RequestHash()
	if _, ok := p.mtx[reqId]; ok {
		return nil
	}
	log.Debug("ProcessContractEvent", "enter, tx req id ", reqId)

	if false == checkTxValid(event.Tx) {
		return errors.New(fmt.Sprintf("ProcessContractEvent recv event Tx is invalid, txid:%s", reqId.String()))
	}
	execBool := p.nodeContractExecutable(p.local, event.Tx)
	p.locker.Lock()
	p.mtx[reqId] = &contractTx{
		reqTx:      event.Tx,
		rstTx:      nil,
		tm:         time.Now(),
		valid:      true,
		executable: execBool, //todo
	}
	p.locker.Unlock()
	log.Debug("ProcessContractEvent", "add tx req id ", reqId)
	ctx := p.mtx[reqId]
	if ctx.executable {
		go p.runContractReq(ctx)
	}
	//broadcast contract request transaction event
	go p.ptn.ContractBroadcast(*event)
	return nil
}

func (p *Processor) getLocalNodesInfo() ([]*nodeInfo, error) {
	if len(p.local) < 1 {
		return nil, errors.New("getLocalNodeInfo, no local account")
	}

	nodes := make([]*nodeInfo, 0)
	for addr, _ := range p.local {
		nodeType := 0
		if p.ptn.IsLocalActiveMediator(addr) {
			nodeType = TMediator
		} else if p.isLocalActiveJury(addr) {
			nodeType = TJury
		}
		node := &nodeInfo{
			addr:  addr,
			ntype: nodeType,
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func localIsMinSigure(tx *modules.Transaction) bool {
	if tx == nil || len(tx.TxMessages) < 3 {
		return false
	}
	for _, msg := range tx.TxMessages {
		if msg.App == modules.APP_SIGNATURE {
			sigPayload := msg.Payload.(*modules.SignaturePayload)
			sigs := sigPayload.Signatures
			localSig := sigs[0].Signature
			if len(sigs) < CONTRACT_SIG_NUM {
				return false
			}
			for _, sig := range sigs {
				if sig.Signature == nil {
					return false
				}
				if bytes.Compare(localSig, sig.Signature) >= 1 {
					return false
				}
			}
			return true
		}
	}
	return false
}

//todo  对于接收到签名交易，而本地合约还未执行完成的情况后面完善
func (p *Processor) ProcessContractSigEvent(event *ContractSigEvent) error {
	if event == nil || len(event.Tx.TxMessages) < 1 {
		return errors.New("ProcessContractSigEvent param is nil")
	}
	if false == checkTxValid(event.Tx) {
		return errors.New("ProcessContractSigEvent event Tx is invalid")
	}
	reqId := event.Tx.RequestHash()
	if _, ok := p.mtx[reqId]; !ok {
		log.Debug("ProcessContractSigEvent", "local not find reqId,create it", reqId.String())
		exec := p.nodeContractExecutable(p.local, event.Tx)
		p.locker.Lock()
		p.mtx[reqId] = &contractTx{
			reqTx:      event.Tx, //todo 只截取请求部分
			tm:         time.Now(),
			valid:      true,
			executable: exec, //default
		}
		ctx := p.mtx[reqId]
		ctx.rcvTx = append(ctx.rcvTx, event.Tx)
		p.locker.Unlock()

		if ctx.executable == true {
			go p.runContractReq(ctx)
		}
		return nil
	}
	ctx := p.mtx[reqId]

	//如果是mediator，检查接收到的签名个数是否达到3个，如果3个，添加到rstTx，否则函数返回
	//如果是jury，将接收到tx与本地执行后的tx进行对比，相同则添加签名到sigTx，如果满足三个签名且签名值最小则广播tx，否则函数返回
	nodes, err := p.getLocalNodesInfo()
	if err == nil || len(nodes) < 1 {
		return errors.New("ProcessContractSigEvent getLocalNodesInfo fail")
	}
	node := nodes[0] //todo mult node
	if node.ntype == TMediator /*node.ntype& TMediator != 0*/ { //mediator
		if getTxSigNum(event.Tx) >= CONTRACT_SIG_NUM {
			ctx.rstTx = event.Tx
		}
	} else if node.ntype == TJury /*node.ntype&TJury != 0*/ { //jury
		if ok, err := checkAndAddTxData(ctx.sigTx, event.Tx); err == nil && ok {
			//获取签名数量，计算hash值是否最小，如果是则广播交易给Mediator,这里采用相同的签名广播接口，即ContractSigMsg
			if getTxSigNum(ctx.sigTx) >= CONTRACT_SIG_NUM {
				//计算hash值是否最小，如果最小则广播该交易
				if localIsMinSigure(ctx.sigTx) == true {
					go p.ptn.ContractSigBroadcast(ContractSigEvent{Tx: ctx.sigTx})
				}
			}
		}
	} else { //default
		log.Info("ProcessContractSigEvent this node don't care this ContractSigEvent")
		return nil
	}

	return errors.New(fmt.Sprintf("ProcessContractSigEvent err with tx id:%s", reqId.String()))
}

func (p *Processor) runContractReq(req *contractTx) error {
	if req == nil {
		return errors.New("runContractReq param is nil")
	}
	_, msgs, err := runContractCmd(p.dag, p.contract, req.reqTx)
	if err != nil {
		log.Error("runContractReq runContractCmd", "reqTx", req.reqTx.RequestHash().String(), "error", err.Error())
		return err
	}
	tx, err := gen.GenContractTransction(req.reqTx, msgs)
	if err != nil {
		log.Error("runContractReq GenContractSigTransctions", "error", err.Error())
		return err
	}

	//如果系统合约，直接添加到缓存池
	//如果用户合约，需要签名，添加到缓存池并广播
	if isSystemContract(tx) {
		req.rstTx = tx
	} else {
		//todo 这里默认取其中一个，实际配置只有一个
		var addr common.Address
		for addr, _ = range p.local {
			break
		}

		sigTx, err := gen.GenContractSigTransction(addr, tx, p.ptn.GetKeyStore())
		if err != nil {
			log.Error("runContractReq GenContractSigTransctions", "error", err.Error())
			return errors.New("")
		}
		req.sigTx = sigTx
		//如果rcvTx存在，则比较执行结果，并将结果附加到sigTx上,并删除rcvTx
		if len(req.rcvTx) > 0 {
			for _, rtx := range req.rcvTx {
				if err := checkAndAddSigSet(req.sigTx, rtx); err != nil {
					log.Error("runContractReq", "checkAndAddSigSet error", err.Error())
				} else {
					log.Debug("runContractReq", "checkAndAddSigSet ok")
				}
			}
			req.rcvTx = nil
		}
		//广播
		go p.ptn.ContractSigBroadcast(ContractSigEvent{Tx: sigTx})
	}
	return nil
}

func checkAndAddSigSet(local *modules.Transaction, recv *modules.Transaction) error {
	if local == nil || recv == nil {
		return errors.New("checkAndAddSigSet param is nil")
	}
	var app modules.MessageType
	for _, msg := range local.TxMessages {
		if msg.App >= modules.APP_CONTRACT_TPL && msg.App <= modules.APP_SIGNATURE {
			app = msg.App
			break
		}
	}
	if app <= 0 {
		return errors.New("checkAndAddSigSet not find contract app type")
	}
	if msgsCompare(local.TxMessages, recv.TxMessages, app) {
		getSigPay := func(mesgs []*modules.Message) *modules.SignaturePayload {
			for _, v := range mesgs {
				if v.App == modules.APP_SIGNATURE {
					return v.Payload.(*modules.SignaturePayload)
				}
			}
			return nil
		}
		localSigPay := getSigPay(local.TxMessages)
		recvSigPay := getSigPay(recv.TxMessages)
		if localSigPay != nil && recvSigPay != nil {
			localSigPay.Signatures = append(localSigPay.Signatures, recvSigPay.Signatures[0])
			log.Debug("checkAndAddSigSet", "local transaction", local.RequestHash(), "recv transaction", recv.RequestHash())
			return nil
		}
	}

	return errors.New("checkAndAddSigSet add sig fail")
}

func (p *Processor) AddContractLoop(txpool txspool.ITxPool, addr common.Address, ks *keystore.KeyStore) error {
	//log.Debug("ProcessContractEvent", "enter", addr.String())
	for _, ctx := range p.mtx {
		if false == ctx.valid || ctx.rstTx == nil {
			continue
		}
		ctx.valid = false
		if false == checkTxValid(ctx.rstTx) {
			log.Error("AddContractLoop recv event Tx is invalid,", "txid", ctx.rstTx.RequestHash().String())
			continue
		}

		tx, err := gen.GenContractSigTransction(addr, ctx.rstTx, ks)
		if err != nil {
			log.Error("AddContractLoop GenContractSigTransctions", "error", err.Error())
			continue
		}
		log.Debug("AddContractLoop", "tx request id", tx.RequestHash().String())
		if err = txpool.AddLocal(txspool.TxtoTxpoolTx(txpool, tx)); err != nil {
			log.Error("AddContractLoop", "error", err.Error())
			continue
		}
	}
	return nil
}

func (p *Processor) CheckContractTxValid(tx *modules.Transaction) bool {
	//检查本地是否存
	if tx == nil {
		log.Error("CheckContractTxValid", "param is nil")
		return false
	}
	reqId := tx.RequestHash()
	log.Debug("CheckContractTxValid", "tx req id ", reqId)

	if false == checkTxValid(tx) {
		log.Error("CheckContractTxValid", "checkTxValid fail")
		return false
	}
	//检查本阶段时候有合约执行权限
	if p.nodeContractExecutable(p.local, tx) != true {
		log.Error("CheckContractTxValid", "nodeContractExecutable false")
		return false
	}

	ctx, ok := p.mtx[reqId]
	if ctx != nil && (ctx.valid == false || ctx.executable == false) {
		return false
	}

	if ok && ctx.rstTx != nil {
		//比较msg
		log.Debug("CheckContractTxValid", "compare txid", reqId)
		return msgsCompare(ctx.rstTx.TxMessages, tx.TxMessages, modules.APP_CONTRACT_INVOKE)
	} else {
		//runContractCmd
		//比较msg
		_, msgs, err := runContractCmd(p.dag, p.contract, tx)
		if err != nil {
			log.Error("CheckContractTxValid runContractCmd", "error", err.Error())
			return false
		}
		p.mtx[reqId].valid = false
		return msgsCompare(msgs, tx.TxMessages, modules.APP_CONTRACT_INVOKE)
	}
}

func (p *Processor) SubscribeContractSigEvent(ch chan<- ContractSigEvent) event.Subscription {
	return p.contractSigScope.Track(p.contractSigFeed.Subscribe(ch))
}

func (p *Processor) ContractTxDeleteLoop() {
	for {
		time.Sleep(time.Second * time.Duration(20))
		p.locker.Lock()
		for k, v := range p.mtx {
			if time.Since(v.tm) > time.Second*100 { //todo
				if v.valid == false {
					log.Info("ContractTxDeleteLoop", "delete tx id", k.String())
					delete(p.mtx, k)
				}
			}
		}
		p.locker.Unlock()
	}
}

//执行合约命令:install、deploy、invoke、stop，同时只支持一种类型
func runContractCmd(dag iDag, contract *contracts.Contract, trs *modules.Transaction) (modules.MessageType, []*modules.Message, error) {
	if trs == nil || len(trs.TxMessages) <= 0 {
		return 0, nil, errors.New("runContractCmd transaction or msg is nil")
	}
	for _, msg := range trs.TxMessages {
		switch msg.App {
		case modules.APP_CONTRACT_TPL_REQUEST:
			{
				msgs := []*modules.Message{}
				req := ContractInstallReq{
					chainID:   "palletone",
					ccName:    msg.Payload.(*modules.ContractInstallRequestPayload).TplName,
					ccPath:    msg.Payload.(*modules.ContractInstallRequestPayload).Path,
					ccVersion: msg.Payload.(*modules.ContractInstallRequestPayload).Version,
				}
				installResult, err := ContractProcess(contract, req)
				if err != nil {
					log.Error("runContractCmd ContractProcess ", "error", err.Error())
					return msg.App, nil, errors.New(fmt.Sprintf("runContractCmd APP_CONTRACT_TPL_REQUEST txid(%s) err:%s", req.ccName, err))
				}
				payload := installResult.(*modules.ContractTplPayload)
				msgs = append(msgs, modules.NewMessage(modules.APP_CONTRACT_TPL, payload))

				return modules.APP_CONTRACT_TPL, msgs, nil
			}
		case modules.APP_CONTRACT_DEPLOY_REQUEST:
			{
				msgs := []*modules.Message{}
				req := ContractDeployReq{
					chainID:    "palletone",
					templateId: msg.Payload.(*modules.ContractDeployRequestPayload).TplId,
					txid:       msg.Payload.(*modules.ContractDeployRequestPayload).TxId,
					args:       msg.Payload.(*modules.ContractDeployRequestPayload).Args,
					timeout:    msg.Payload.(*modules.ContractDeployRequestPayload).Timeout,
				}
				deployResult, err := ContractProcess(contract, req)
				if err != nil {
					log.Error("runContractCmd ContractProcess ", "error", err.Error())
					return msg.App, nil, errors.New(fmt.Sprintf("runContractCmd APP_CONTRACT_DEPLOY_REQUEST TplId(%s) err:%s", req.templateId, err))
				}
				payload := deployResult.(*modules.ContractDeployPayload)
				msgs = append(msgs, modules.NewMessage(modules.APP_CONTRACT_DEPLOY, payload))
				return modules.APP_CONTRACT_DEPLOY, nil, nil
			}
		case modules.APP_CONTRACT_INVOKE_REQUEST:
			{
				msgs := []*modules.Message{}
				req := ContractInvokeReq{
					chainID:  "palletone",
					deployId: msg.Payload.(*modules.ContractInvokeRequestPayload).ContractId,
					args:     msg.Payload.(*modules.ContractInvokeRequestPayload).Args,
					txid:     trs.RequestHash().String(),
				}
				//对msg0进行修改
				fullArgs, err := handleMsg0(trs, dag, req.args)
				if err != nil {
					return modules.APP_CONTRACT_INVOKE, nil, err
				}
				req.args = fullArgs

				invokeResult, err := ContractProcess(contract, req)
				if err != nil {
					log.Error("runContractCmd ContractProcess", "ContractProcess error", err.Error())
					return msg.App, nil, errors.New(fmt.Sprintf("runContractCmd APP_CONTRACT_INVOKE txid(%s) rans err:%s", req.txid, err))
				}
				result := invokeResult.(*modules.ContractInvokeResult)
				payload := modules.NewContractInvokePayload(result.ContractId, result.FunctionName, result.Args, result.ExecutionTime, result.ReadSet, result.WriteSet, result.Payload)

				if payload != nil {
					msgs = append(msgs, modules.NewMessage(modules.APP_CONTRACT_INVOKE, payload))
				}
				toContractPayments, err := resultToContractPayments(dag, result)
				if err != nil {
					return modules.APP_CONTRACT_INVOKE, nil, err
				}
				if toContractPayments != nil && len(toContractPayments) > 0 {
					for _, contractPayment := range toContractPayments {
						msgs = append(msgs, modules.NewMessage(modules.APP_PAYMENT, contractPayment))
					}
				}
				cs, err := resultToCoinbase(result)
				if err != nil {
					return modules.APP_CONTRACT_INVOKE, nil, err
				}
				if cs != nil && len(cs) > 0 {
					for _, coinbase := range cs {
						msgs = append(msgs, modules.NewMessage(modules.APP_PAYMENT, coinbase))
					}
				}

				return modules.APP_CONTRACT_INVOKE, msgs, nil
			}
		case modules.APP_CONTRACT_STOP_REQUEST:
			{
				msgs := []*modules.Message{}
				req := ContractStopReq{
					chainID:     "palletone",
					deployId:    msg.Payload.(*modules.ContractStopRequestPayload).ContractId,
					txid:        msg.Payload.(*modules.ContractStopRequestPayload).Txid,
					deleteImage: msg.Payload.(*modules.ContractStopRequestPayload).DeleteImage,
				}
				_, err := ContractProcess(contract, req) //todo
				if err != nil {
					log.Error("runContractCmd ContractProcess ", "error", err.Error())
					return msg.App, nil, errors.New(fmt.Sprintf("runContractCmd APP_CONTRACT_STOP_REQUEST contractId(%s) err:%s", req.deployId, err))
				}
				//payload := stopResult.(*modules.ContractStopPayload)
				//msgs = append(msgs, modules.NewMessage(modules.APP_CONTRACT_STOP, payload))
				return modules.APP_CONTRACT_STOP, msgs, nil
			}
		}
	}

	return 0, nil, errors.New(fmt.Sprintf("runContractCmd err, txid=%s", trs.RequestHash().String()))
}

func handleMsg0(tx *modules.Transaction, dag iDag, reqArgs [][]byte) ([][]byte, error) {
	var txArgs [][]byte
	invokeInfo := modules.InvokeInfo{}
	if len(tx.TxMessages) > 0 {
		msg0 := tx.TxMessages[0].Payload.(*modules.PaymentPayload)
		invokeAddr, err := dag.GetAddrByOutPoint(msg0.Inputs[0].PreviousOutPoint)
		if err != nil {
			return nil, err
		}
		//如果是交付保证金
		//if string(reqArgs[0]) == "DepositWitnessPay" {
		invokeTokens := &modules.InvokeTokens{}
		outputs := msg0.Outputs
		invokeTokens.Asset = outputs[0].Asset
		for _, output := range outputs {
			addr, err := tokenengine.GetAddressFromScript(output.PkScript)
			if err != nil {
				return nil, err
			}
			contractAddr, err := common.StringToAddress("PCGTta3M4t3yXu8uRgkKvaWd2d8DR32W9vM")
			if err != nil {
				return nil, err
			}
			if addr.Equal(contractAddr) {
				invokeTokens.Amount += output.Value
			}
		}
		invokeInfo.InvokeTokens = invokeTokens
		//}
		invokeFees, err := dag.GetTxFee(tx)
		if err != nil {
			return nil, err
		}

		//invokeInfo = unit.InvokeInfo{
		//	InvokeAddress: invokeAddr,
		//	InvokeFees:    invokeFees,
		//}
		invokeInfo.InvokeAddress = invokeAddr.String()
		invokeInfo.InvokeFees = invokeFees

		invokeInfoBytes, err := json.Marshal(invokeInfo)
		if err != nil {
			return nil, err
		}
		txArgs = append(txArgs, invokeInfoBytes)
	} else {
		invokeInfoBytes, err := json.Marshal(invokeInfo)
		if err != nil {
			return nil, err
		}
		txArgs = append(txArgs, invokeInfoBytes)
	}
	txArgs = append(txArgs, reqArgs...)
	//reqArgs = append(reqArgs, txArgs...)
	return txArgs, nil
}

func checkAndAddTxData(local *modules.Transaction, recv *modules.Transaction) (bool, error) {
	var recvSigMsg *modules.Message

	if local == nil || recv == nil {
		return false, errors.New("checkAndAddTxData param is nil")
	}
	if len(local.TxMessages) != len(recv.TxMessages) {
		return false, errors.New("checkAndAddTxData tx msg is invalid")
	}
	for i := 0; i < len(local.TxMessages); i++ {
		if recv.TxMessages[i].App == modules.APP_SIGNATURE {
			recvSigMsg = recv.TxMessages[i]
		} else if reflect.DeepEqual(*local.TxMessages[i], *recv.TxMessages[i]) != true {
			return false, errors.New("checkAndAddTxData tx msg is not equal")
		}
	}

	if recvSigMsg == nil {
		return false, errors.New("checkAndAddTxData not find recv sig msg")
	}
	for i, msg := range local.TxMessages {
		if msg.App == modules.APP_SIGNATURE {
			sigPayload := msg.Payload.(*modules.SignaturePayload)
			sigs := sigPayload.Signatures
			for _, sig := range sigs {
				if true == bytes.Equal(sig.PubKey, recvSigMsg.Payload.(*modules.SignaturePayload).Signatures[0].PubKey) &&
					true == bytes.Equal(sig.Signature, recvSigMsg.Payload.(*modules.SignaturePayload).Signatures[0].Signature) {
					log.Info("checkAndAddTxData tx  already recv:", recv.RequestHash().String())
					return false, nil
				}
			}
			//直接将签名添加到msg中
			if len(recvSigMsg.Payload.(*modules.SignaturePayload).Signatures) > 0 {
				sigPayload.Signatures = append(sigs, recvSigMsg.Payload.(*modules.SignaturePayload).Signatures[0])
			}
			local.TxMessages[i].Payload = sigPayload
			log.Info("checkAndAddTxData", "add sig payload:", sigPayload.Signatures)
			return true, nil
		}
	}

	return false, errors.New("checkAndAddTxData fail")
}

func getTxSigNum(tx *modules.Transaction) int {
	if tx != nil {
		for _, msg := range tx.TxMessages {
			if msg.App == modules.APP_SIGNATURE {
				return len(msg.Payload.(*modules.SignaturePayload).Signatures)
			}
		}
	}
	return 0
}

func checkTxValid(tx *modules.Transaction) bool {
	return cm.ValidateTxSig(tx)
}

func msgsCompare(msgsA []*modules.Message, msgsB []*modules.Message, msgType modules.MessageType) bool {
	if msgsA == nil || msgsB == nil {
		log.Error("msgsCompare", "param is nil")
		return false
	}
	var msg1, msg2 *modules.Message
	for _, v := range msgsA {
		if v.App == msgType {
			msg1 = v
		}
	}
	for _, v := range msgsB {
		if v.App == msgType {
			msg2 = v
		}
	}

	if msg1 != nil && msg2 != nil {
		if reflect.DeepEqual(msg1, msg2) == true {
			log.Debug("msgsCompare", "msg is equal, type", msgType)
			return true
		}
	}
	log.Debug("msgsCompare", "msg is not equal")

	return false
}

func isSystemContract(tx *modules.Transaction) bool {
	//if tx == nil{
	//	return true, errors.New("isSystemContract param is nil")
	//}

	for _, msg := range tx.TxMessages {
		if msg.App == modules.APP_CONTRACT_INVOKE_REQUEST {
			contractId := msg.Payload.(*modules.ContractInvokeRequestPayload).ContractId
			log.Debug("nodeContractExecutable", "contract id", contractId, "len", len(contractId))
			contractAddr := common.NewAddress(contractId, common.ContractHash)
			return contractAddr.IsSystemContractAddress() //, nil

		} else if msg.App >= modules.APP_CONTRACT_TPL_REQUEST {
			return false //, nil
		}
	}
	return true //, errors.New("isSystemContract not find contract type")
}

func (p *Processor) nodeContractExecutable( accounts map[common.Address]*JuryAccount /*addrs []common.Address*/ , tx *modules.Transaction) bool {
	if tx == nil {
		return false
	}
	sysContract := isSystemContract(tx)
	if sysContract { //system contract
		for addr, _ := range accounts {
			if p.ptn.IsLocalActiveMediator(addr) {
				log.Debug("nodeContractExecutable", "Mediator, true:tx requestId", tx.RequestHash())
				return true
			}
		}
	} else { //usr contract
		log.Debug("User contract, call docker to run contract.")
		for addr, _ := range accounts {
			if true == p.isLocalActiveJury(addr) {
				log.Debug("nodeContractExecutable", "Jury, true:tx requestId", tx.RequestHash())
				return true
			}
		}
	}
	log.Debug("nodeContractExecutable", "false:tx requestId", tx.RequestHash())

	return false
}

func (p *Processor) addTx2LocalTxTool(tx *modules.Transaction, cnt int) error {
	if tx == nil || cnt < 4 {
		return errors.New(fmt.Sprintf("addTx2LocalTxTool param error, node count is [%d]", cnt))
	}
	if num := getTxSigNum(tx); num < (cnt*2/3 + 1) {
		log.Error("addTx2LocalTxTool sig num is", num)
		return errors.New(fmt.Sprintf("addTx2LocalTxTool tx sig num is:%d", num))
	}

	txPool := p.ptn.TxPool()
	log.Debug("addTx2LocalTxTool", "tx:", tx.Hash().String())

	return txPool.AddLocal(txspool.TxtoTxpoolTx(txPool, tx))
}

func (p *Processor) ContractTxCreat(deployId []byte, txBytes []byte, args [][]byte, timeout time.Duration) (rspPayload []byte, err error) {
	log.Info("ContractTxCreat", fmt.Sprintf("enter, deployId[%v],", deployId))

	if deployId == nil || args == nil {
		log.Error("ContractTxCreat", "param is nil")
		return nil, errors.New("transaction request param is nil")
	}

	tx := &modules.Transaction{}
	if txBytes != nil {
		if err := rlp.DecodeBytes(txBytes, tx); err != nil {
			return nil, err
		}
	} else {
		pay := &modules.PaymentPayload{
			Inputs:   []*modules.Input{},
			Outputs:  []*modules.Output{},
			LockTime: 11111, //todo
		}
		msgPay := &modules.Message{
			App:     modules.APP_PAYMENT,
			Payload: pay,
		}
		tx.AddMessage(msgPay)
	}

	msgReq := &modules.Message{
		App: modules.APP_CONTRACT_INVOKE_REQUEST,
		Payload: &modules.ContractInvokeRequestPayload{
			ContractId: deployId,
			Args:       args,
			Timeout:    timeout,
		},
	}

	tx.AddMessage(msgReq)

	return rlp.EncodeToBytes(tx)
}

func (p *Processor) ContractTxBroadcast(txBytes []byte) ([]byte, error) {
	if txBytes == nil {
		log.Error("ContractTxBroadcast", "param is nil")
		return nil, errors.New("transaction request param is nil")
	}
	log.Info("ContractTxBroadcast enter")

	tx := &modules.Transaction{}
	if err := rlp.DecodeBytes(txBytes, tx); err != nil {
		return nil, err
	}

	req := tx.RequestHash()
	p.locker.Lock()
	p.mtx[req] = &contractTx{
		reqTx:      tx,
		tm:         time.Now(),
		valid:      true,
		executable: true, //default
	}
	p.locker.Unlock()
	go p.ptn.ContractBroadcast(ContractExeEvent{Tx: tx})

	return req[:], nil
}

//tmp
func (p *Processor) creatContractTxReqBroadcast(from, to common.Address, daoAmount, daoFee uint64, msg *modules.Message) ([]byte, error) {
	tx, _, err := p.dag.CreateGenericTransaction(from, to, daoAmount, daoFee, msg)
	if err != nil {
		return nil, err
	}
	log.Debug("creatContractTxReq", "tx:", tx)

	//tx.AddMessage(msg)
	tx, err = p.ptn.SignGenericTransaction(from, tx)
	if err != nil {
		return nil, err
	}
	reqId := tx.RequestHash()
	p.locker.Lock()
	p.mtx[reqId] = &contractTx{
		reqTx:      tx,
		tm:         time.Now(),
		valid:      true,
		executable: true, //default
	}
	p.locker.Unlock()
	txHex, _ := rlp.EncodeToBytes(tx)
	log.Debugf("Signed ContractRequest hex:%x", txHex)
	if p.mtx[reqId].executable {
		if p.nodeContractExecutable(p.local, tx) == true {
			go p.runContractReq(p.mtx[reqId])
		}
	}
	//broadcast
	go p.ptn.ContractBroadcast(ContractExeEvent{Tx: tx})
	//local
	//go p.contractExecFeed.Send(ContractExeEvent{modules.NewTransaction([]*modules.Message{msgPay, msgReq})})
	//go p.ProcessContractEvent(&ContractExeEvent{Tx: tx})

	return reqId[:], nil
}

func (p *Processor) ContractInstallReq(from, to common.Address, daoAmount, daoFee uint64, tplName, path, version string) ([]byte, error) {
	if from == (common.Address{}) || to == (common.Address{}) || tplName == "" || path == "" || version == "" {
		log.Error("ContractInstallReq", "param is error")
		return nil, errors.New("ContractInstallReq request param is error")
	}

	log.Debug("ContractInstallReq", "enter, tplName ", tplName, "path", path, "version", version)
	msgReq := &modules.Message{
		App: modules.APP_CONTRACT_TPL_REQUEST,
		Payload: &modules.ContractInstallRequestPayload{
			TplName: tplName,
			Path:    path,
			Version: version,
		},
	}
	return p.creatContractTxReqBroadcast(from, to, daoAmount, daoFee, msgReq)
}

func (p *Processor) ContractDeployReq(from, to common.Address, daoAmount, daoFee uint64, templateId []byte, txid string, args [][]byte, timeout time.Duration) ([]byte, error) {
	if from == (common.Address{}) || to == (common.Address{}) || templateId == nil {
		log.Error("ContractDeployReq", "param is error")
		return nil, errors.New("ContractDeployReq request param is error")
	}
	log.Debug("ContractDeployReq", "enter, templateId ", templateId)

	msgReq := &modules.Message{
		App: modules.APP_CONTRACT_DEPLOY_REQUEST,
		Payload: &modules.ContractDeployRequestPayload{
			TplId:   templateId,
			TxId:    txid,
			Args:    args,
			Timeout: timeout,
		},
	}
	return p.creatContractTxReqBroadcast(from, to, daoAmount, daoFee, msgReq)
}

func (p *Processor) ContractInvokeReq(from, to common.Address, daoAmount, daoFee uint64, contractId common.Address, args [][]byte, timeout time.Duration) ([]byte, error) {
	if from == (common.Address{}) || to == (common.Address{}) || contractId == (common.Address{}) || args == nil {
		log.Error("ContractInvokeReq", "param is error")
		return nil, errors.New("ContractInvokeReq request param is error")
	}

	log.Debug("ContractInvokeReq", "enter, contractId ", contractId)
	msgReq := &modules.Message{
		App: modules.APP_CONTRACT_INVOKE_REQUEST,
		Payload: &modules.ContractInvokeRequestPayload{
			ContractId:   contractId.Bytes(),
			FunctionName: "",
			Args:         args,
			Timeout:      timeout,
		},
	}
	return p.creatContractTxReqBroadcast(from, to, daoAmount, daoFee, msgReq)
}

func (p *Processor) ContractStopReq(from, to common.Address, daoAmount, daoFee uint64, contractId common.Address, txid string, deleteImage bool) ([]byte, error) {
	if from == (common.Address{}) || to == (common.Address{}) || contractId == (common.Address{}) {
		log.Error("ContractStopReq", "param is error")
		return nil, errors.New("ContractStopReq request param is error")
	}

	log.Debug("ContractStopReq", "enter, contractId ", contractId)
	msgReq := &modules.Message{
		App: modules.APP_CONTRACT_STOP_REQUEST,
		Payload: &modules.ContractStopRequestPayload{
			ContractId:  contractId[:],
			Txid:        txid,
			DeleteImage: deleteImage,
		},
	}
	return p.creatContractTxReqBroadcast(from, to, daoAmount, daoFee, msgReq)
}

func printTxInfo(tx *modules.Transaction) {
	if tx == nil {
		return
	}

	log.Info("=========tx info============hash:", tx.Hash().String())
	for i := 0; i < len(tx.TxMessages); i++ {
		log.Info("---------")
		app := tx.TxMessages[i].App
		pay := tx.TxMessages[i].Payload
		log.Info("", "app:", app)
		if app == modules.APP_PAYMENT {
			p := pay.(*modules.PaymentPayload)
			fmt.Println(p.LockTime)
		} else if app == modules.APP_CONTRACT_INVOKE_REQUEST {
			p := pay.(*modules.ContractInvokeRequestPayload)
			fmt.Println(p.ContractId)
		} else if app == modules.APP_CONTRACT_INVOKE {
			p := pay.(*modules.ContractInvokePayload)
			fmt.Println(p.Args)
			for idx, v := range p.WriteSet {
				fmt.Printf("WriteSet:idx[%d], k[%v]-v[%v]", idx, v.Key, v.Value)
			}
			for idx, v := range p.ReadSet {
				fmt.Printf("ReadSet:idx[%d], k[%v]-v[%v]", idx, v.Key, v.Value)
			}
		} else if app == modules.APP_SIGNATURE {
			p := pay.(*modules.SignaturePayload)
			fmt.Printf("Signatures:[%v]", p.Signatures)
		}
	}
}
