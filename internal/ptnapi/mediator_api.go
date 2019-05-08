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
 * @author PalletOne core developer Albert·Gou <dev@pallet.one>
 * @date 2018/11/05
 */

package ptnapi

import (
	"fmt"
	"time"

	"github.com/palletone/go-palletone/common"
	"github.com/palletone/go-palletone/common/p2p/discover"
	"github.com/palletone/go-palletone/core"
	dagcom "github.com/palletone/go-palletone/dag/common"
	"github.com/palletone/go-palletone/dag/modules"
)

type PublicMediatorAPI struct {
	Backend
}

func NewPublicMediatorAPI(b Backend) *PublicMediatorAPI {
	return &PublicMediatorAPI{b}
}

func (a *PublicMediatorAPI) GetList() []string {
	addStrs := make([]string, 0)
	mas := a.Dag().GetMediators()

	for address, _ := range mas {
		addStrs = append(addStrs, address.Str())
	}

	return addStrs
}

func (a *PublicMediatorAPI) ListVoteResults() map[string]uint64 {
	mediatorVoteCount := make(map[string]uint64)

	for address, _ := range a.Dag().GetMediators() {
		mediatorVoteCount[address.String()] = 0
	}

	for med, stake := range a.Dag().MediatorVotedResults() {
		mediatorVoteCount[med] = stake
	}

	return mediatorVoteCount
}

func (a *PublicMediatorAPI) GetActives() []string {
	addStrs := make([]string, 0)
	ms := a.Dag().ActiveMediators()

	for medAdd, _ := range ms {
		addStrs = append(addStrs, medAdd.Str())
	}

	return addStrs
}

func (a *PublicMediatorAPI) GetVoted(addStr string) ([]string, error) {
	addr, err := common.StringToAddress(addStr)
	if err != nil {
		return nil, err
	}

	voted := a.Dag().GetAccountVotedMediators(addr)
	mediators := make([]string, 0, len(voted))

	for _, med := range voted {
		mediators = append(mediators, med.Str())
	}

	return mediators, nil
}

func (a *PublicMediatorAPI) GetNextUpdateTime() string {
	dgp := a.Dag().GetDynGlobalProp()
	time := time.Unix(int64(dgp.NextMaintenanceTime), 0)

	return time.Format("2006-01-02 15:04:05")
}

func (a *PublicMediatorAPI) GetInfo(addStr string) (*modules.MediatorInfo, error) {
	mediator, err := common.StringToAddress(addStr)
	if err != nil {
		return nil, err
	}

	if !a.Dag().IsMediator(mediator) {
		return nil, fmt.Errorf("%v is not mediator", mediator.Str())
	}

	return a.Dag().GetMediatorInfo(mediator), nil
}

const DefaultResult = "Transaction executed locally, but may not be confirmed by the network yet!"

type PrivateMediatorAPI struct {
	Backend
}

func NewPrivateMediatorAPI(b Backend) *PrivateMediatorAPI {
	return &PrivateMediatorAPI{b}
}

// 交易执行结果
type TxExecuteResult struct {
	TxContent string      `json:"txContent"`
	TxHash    common.Hash `json:"txHash"`
	TxSize    string      `json:"txSize"`
	TxFee     string      `json:"txFee"`
	//Tip       string      `json:"tip"`
	Warning string `json:"warning"`
}

// 创建 mediator 所需的参数, 至少包含普通账户地址
type MediatorCreateArgs struct {
	*modules.MediatorCreateOperation
}

// 相关参数检查
func (args *MediatorCreateArgs) setDefaults(node *discover.Node) (initPrivKey string) {
	if args.InitPubKey == "" {
		initPrivKey, args.InitPubKey = core.CreateInitDKS()
	}

	//if args.Node == "" {
	//	args.Node = node.String()
	//}

	return
}

func (a *PrivateMediatorAPI) Create(args MediatorCreateArgs) (*TxExecuteResult, error) {
	// 参数补全
	//initPrivKey := args.setDefaults(a.srvr.Self())

	// 参数验证
	err := args.Validate()
	if err != nil {
		return nil, err
	}

	// 判断本节点是否同步完成，数据是否最新
	//if !a.dag.IsSynced() {
	//	return nil, fmt.Errorf("the data of this node is not synced, " +
	//		"and mediator cannot be created at present")
	//}

	addr := args.FeePayer()
	// 判断是否已经是mediator
	if a.Dag().IsMediator(addr) {
		return nil, fmt.Errorf("account %v is already a mediator", args.AddStr)
	}

	// 判断是否申请通过
	if !dagcom.MediatorCreateEvaluate(args.MediatorCreateOperation) {
		return nil, fmt.Errorf("has not successfully paid the deposit")
	}

	// 1. 创建交易
	tx, fee, err := a.Dag().GenMediatorCreateTx(addr, args.MediatorCreateOperation, a.TxPool())
	if err != nil {
		return nil, err
	}

	// 2. 签名和发送交易
	err = a.SignAndSendTransaction(addr, tx)
	if err != nil {
		return nil, err
	}

	// 5. 返回执行结果
	res := &TxExecuteResult{}
	res.TxContent = fmt.Sprintf("Create mediator %v with initPubKey : %v , node: %v , url: %v",
		args.AddStr, args.InitPubKey, args.Node, args.Url)
	res.TxHash = tx.Hash()
	res.TxSize = tx.Size().TerminalString()
	res.TxFee = fmt.Sprintf("%vdao", fee)
	res.Warning = DefaultResult

	//if initPrivKey != "" {
	//	res.Tip = "Your initial private key is: " + initPrivKey + " , initial public key is: " +
	//		args.InitPubKey + " , please keep in mind!"
	//}

	return res, nil
}

func (a *PrivateMediatorAPI) Vote(voterStr string, mediatorStrs []string) (*TxExecuteResult, error) {
	// 参数检查
	voter, err := common.StringToAddress(voterStr)
	if err != nil {
		return nil, fmt.Errorf("invalid account address: %v", voterStr)
	}

	// 判断本节点是否同步完成，数据是否最新
	//if !a.dag.IsSynced() {
	//	return nil, fmt.Errorf("the data of this node is not synced, and can't vote now")
	//}

	mediators := make([]common.Address, 0, len(mediatorStrs))
	for _, mediatorStr := range mediatorStrs {
		mediator, err := common.StringToAddress(mediatorStr)
		if err != nil {
			return nil, fmt.Errorf("invalid account address: %v", mediatorStr)
		}

		// 判断是否是mediator
		if !a.Dag().IsMediator(mediator) {
			return nil, fmt.Errorf("%v is not mediator", mediatorStr)
		}

		mediators = append(mediators, mediator)
	}

	// 1. 创建交易
	tx, fee, err := a.Dag().GenVoteMediatorTx(voter, mediators, a.TxPool())
	if err != nil {
		return nil, err
	}

	// 2. 签名和发送交易
	err = a.SignAndSendTransaction(voter, tx)
	if err != nil {
		return nil, err
	}

	// 5. 返回执行结果
	res := &TxExecuteResult{}
	res.TxContent = fmt.Sprintf("Account %v vote mediator %v", voterStr, mediatorStrs)
	res.TxHash = tx.Hash()
	res.TxSize = tx.Size().TerminalString()
	res.TxFee = fmt.Sprintf("%vdao", fee)
	res.Warning = DefaultResult

	return res, nil
}
