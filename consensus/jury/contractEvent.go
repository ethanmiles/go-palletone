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
	"fmt"
	"time"

	"github.com/palletone/go-palletone/common/event"
	"github.com/palletone/go-palletone/common/log"
	"github.com/palletone/go-palletone/dag/errors"
	"github.com/palletone/go-palletone/dag/modules"
)

func (p *Processor) SubscribeContractEvent(ch chan<- ContractEvent) event.Subscription {
	return p.contractExecScope.Track(p.contractExecFeed.Subscribe(ch))
}

func (p *Processor) ProcessContractEvent(event *ContractEvent) error {
	if event == nil || event.Tx == nil || len(event.Tx.TxMessages) < 1 {
		return errors.New("ProcessContractEvent param is nil")
	}
	reqId := event.Tx.RequestHash()
	if p.checkTxIsExist(event.Tx) {
		err := fmt.Sprintf("[%s]ProcessContractEvent, event Tx is exist, txId:%s", shortId(reqId.String()), event.Tx.Hash().String())
		return errors.New(err)
	}
	if p.checkTxReqIdIsExist(reqId) {
		err := fmt.Sprintf("[%s]ProcessContractEvent, event Tx reqId is exist, txId:%s", shortId(reqId.String()), event.Tx.Hash().String())
		return errors.New(err)
	}
	if !p.checkTxValid(event.Tx) {
		err := fmt.Sprintf("[%s]ProcessContractEvent, event Tx is invalid, txId:%s", shortId(reqId.String()), event.Tx.Hash().String())
		return errors.New(err)
	}
	if !p.contractEventExecutable(event.CType, event.Tx, event.Ele) {
		log.Debugf("[%s]ProcessContractEvent, contractEventExecutable is false", shortId(reqId.String()))
		return nil
	}
	log.Debugf("[%s]ProcessContractEvent, event type:%v ", shortId(reqId.String()), event.CType)
	broadcast := false
	var err error
	switch event.CType {
	case CONTRACT_EVENT_EXEC:
		broadcast, err = p.contractExecEvent(event.Tx, event.Ele)
	case CONTRACT_EVENT_SIG:
		broadcast, err = p.contractSigEvent(event.Tx, event.Ele)
	case CONTRACT_EVENT_COMMIT:
		broadcast, err = p.contractCommitEvent(event.Tx)
	case CONTRACT_EVENT_ELE:
		return p.contractEleEvent(event.Tx)
	}
	if broadcast {
		go p.ptn.ContractBroadcast(*event, false)
	}
	return err
}

func (p *Processor) contractEleEvent(tx *modules.Transaction) error {
	p.locker.Lock()
	defer p.locker.Unlock()

	reqId := tx.RequestHash()
	if _, ok := p.mtx[reqId]; !ok {
		p.mtx[reqId] = &contractTx{
			reqTx:  tx.GetRequestTx(),
			rstTx:  nil,
			valid:  true,
			tm:     time.Now(),
			adaInf: make(map[uint32]*AdapterInf),
		}
	}
	mtx := p.mtx[reqId]
	eles, err := p.getContractAssignElectionList(tx)
	if err != nil {
		return err
	}
	elesLen := len(eles)
	if elesLen > 0 {
		if elesLen >= p.electionNum {
			mtx.eleInf = eles[0:p.electionNum]
			log.Debugf("[%s]contractEleEvent election Num ok", shortId(reqId.String()))
		} else {
			mtx.eleInf = eles[:]
		}
	}
	if _, ok := p.mel[reqId]; !ok {
		p.mel[reqId] = &electionVrf{
			rcvEle: make([]modules.ElectionInf, 0),
			sigs:   make([]modules.SignatureSet, 0),
			tm:     time.Now(),
		}
	}
	if elesLen < p.electionNum {
		reqEvent := &ElectionRequestEvent{
			ReqId: reqId,
		}
		go p.ptn.ElectionBroadcast(ElectionEvent{EType: ELECTION_EVENT_VRF_REQUEST, Event: reqEvent}, true)
	}
	return nil
}

func (p *Processor) contractExecEvent(tx *modules.Transaction, ele []modules.ElectionInf) (broadcast bool, err error) {
	reqId := tx.RequestHash()
	p.locker.Lock()
	if p.mtx[reqId] == nil {
		p.mtx[reqId] = &contractTx{
			rstTx:  nil,
			tm:     time.Now(),
			valid:  true,
			adaInf: make(map[uint32]*AdapterInf),
		}
	} else {
		if p.mtx[reqId].reqRcvEd {
			p.locker.Unlock()
			return false, nil
		}
	}
	p.mtx[reqId].reqTx = tx.GetRequestTx()
	p.mtx[reqId].eleInf = ele
	p.mtx[reqId].reqRcvEd = true
	//关闭mel
	if e, ok := p.mel[reqId]; ok {
		e.invalid = true
	}
	p.locker.Unlock()
	log.Debugf("[%s]contractExecEvent, add tx reqId:%s", shortId(reqId.String()), reqId.String())

	if !tx.IsSystemContract() { //系统合约在UNIT构建前执行
		go p.runContractReq(reqId, ele)
	}
	return true, nil
}

func (p *Processor) contractSigEvent(tx *modules.Transaction, ele []modules.ElectionInf) (broadcast bool, err error) {
	p.locker.Lock()
	defer p.locker.Unlock()
	reqId := tx.RequestHash()
	if _, ok := p.mtx[reqId];ok {
		if checkTxReceived(p.mtx[reqId].rcvTx, tx) {
			return false, nil
		}
	}
	log.Debugf("[%s]contractSigEvent, receive sig tx[%s]", shortId(reqId.String()), tx.Hash().String())
	if _, ok := p.mtx[reqId]; !ok {
		log.Debugf("[%s]contractSigEvent, local not find reqId,create it", shortId(reqId.String()))
		p.mtx[reqId] = &contractTx{
			reqTx:  tx.GetRequestTx(),
			eleInf: ele,
			tm:     time.Now(),
			valid:  true,
			adaInf: make(map[uint32]*AdapterInf),
		}
		p.mtx[reqId].rcvTx = append(p.mtx[reqId].rcvTx, tx)
		//go p.runContractReq(reqId, ele) //del
		return true, nil
	}
	ctx := p.mtx[reqId]
	ctx.rcvTx = append(ctx.rcvTx, tx)

	//如果是jury，将接收到tx与本地执行后的tx进行对比，相同则添加签名到sigTx，如果满足签名数量且签名值最小则广播tx，否则函数返回
	if ok, err := checkAndAddTxSigMsgData(ctx.sigTx, tx); err == nil && ok {
		if getTxSigNum(ctx.sigTx) >= p.contractSigNum {
			if localIsMinSignature(ctx.sigTx) { //todo
				//签名数量足够，而且当前节点是签名最新的节点，那么合并签名并广播完整交易
				log.Infof("[%s]runContractReq, localIsMinSignature Ok!", shortId(reqId.String()))
				processContractPayout(ctx.sigTx, ele)
				go p.ptn.ContractBroadcast(ContractEvent{Ele: ele, CType: CONTRACT_EVENT_COMMIT, Tx: ctx.sigTx}, true)
			}
		}
	} else if err != nil {
		return true, err
	}
	return true, nil
}

func (p *Processor) contractCommitEvent(tx *modules.Transaction) (broadcast bool, err error) {
	reqId := tx.RequestHash()
	p.locker.Lock()
	defer p.locker.Unlock()
	if _, ok := p.mtx[reqId]; !ok {
		//log.Debug("contractCommitEvent", "local not find reqId,create it", reqId)
		p.mtx[reqId] = &contractTx{
			reqTx:  tx.GetRequestTx(),
			tm:     time.Now(),
			valid:  true,
			adaInf: make(map[uint32]*AdapterInf),
		}
	} else if p.mtx[reqId].rstTx != nil {
		log.Debugf("[%s]contractCommitEvent, rstTx already receive", shortId(reqId.String()))
		return false, nil //rstTx already receive
	}
	p.mtx[reqId].valid = true
	p.mtx[reqId].rstTx = tx

	return true, nil
}
