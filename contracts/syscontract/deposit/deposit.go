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

//  Package deposit implements some functions for deposit contract.
package deposit

import (
	"encoding/json"

	"github.com/palletone/go-palletone/common/log"
	"github.com/palletone/go-palletone/contracts/shim"
	pb "github.com/palletone/go-palletone/core/vmContractPub/protos/peer"
	"github.com/palletone/go-palletone/dag/constants"
	"github.com/palletone/go-palletone/dag/modules"
)

type DepositChaincode struct {
}

func (d *DepositChaincode) Init(stub shim.ChaincodeStubInterface) pb.Response {
	log.Info("*** DepositChaincode system contract init ***")
	return shim.Success([]byte("init ok"))
}

func (d *DepositChaincode) Invoke(stub shim.ChaincodeStubInterface) pb.Response {
	funcName, args := stub.GetFunctionAndParameters()
	switch funcName {
	//
	// 申请成为Mediator
	case modules.ApplyMediator:
		log.Info("Enter DepositChaincode Contract " + modules.ApplyMediator + " Invoke")
		return d.applyBecomeMediator(stub, args)
	// mediator 交付保证金
	case modules.MediatorPayDeposit:
		log.Info("Enter DepositChaincode Contract " + modules.MediatorPayDeposit + " Invoke")
		return d.mediatorPayToDepositContract(stub, args)
	// 申请退出Mediator
	case modules.MediatorApplyQuit:
		log.Info("Enter DepositChaincode Contract " + modules.MediatorApplyQuit + " Invoke")
		return d.mediatorApplyQuit(stub, args)
	// 更新 Mediator 信息
	case modules.UpdateMediatorInfo:
		log.Info("Enter DepositChaincode Contract " + modules.UpdateMediatorInfo + " Invoke")
		return d.UpdateMediatorInfo(stub, args)
	//
	//  jury 交付保证金
	case JuryPayToDepositContract:
		log.Info("Enter DepositChaincode Contract " + JuryPayToDepositContract + " Invoke")
		return d.juryPayToDepositContract(stub, args)
		//  jury 申请退出
	case JuryApplyQuit:
		log.Info("Enter DepositChaincode Contract " + JuryApplyQuit + " Invoke")
		return d.juryApplyQuit(stub, args)
	//
	//  developer 交付保证金
	case DeveloperPayToDepositContract:
		log.Info("Enter DepositChaincode Contract " + DeveloperPayToDepositContract + " Invoke")
		return d.developerPayToDepositContract(stub, args)
		//  developer 申请退出
	case DeveloperApplyQuit:
		log.Info("Enter DepositChaincode Contract " + DeveloperApplyQuit + " Invoke")
		return d.devApplyQuit(stub, args)
	//
	//  基金会对加入申请Mediator进行处理
	case HandleForApplyBecomeMediator:
		log.Info("Enter DepositChaincode Contract " + HandleForApplyBecomeMediator + " Invoke")
		return d.handleForApplyBecomeMediator(stub, args)
	//  基金会移除某个节点
	case HanldeNodeRemoveFromAgreeList:
		log.Info("Enter DepositChaincode Contract " + HanldeNodeRemoveFromAgreeList + " Invoke")
		return d.handleNodeRemoveFromAgreeList(stub, args)
		//  基金会对退出申请Mediator进行处理
	case HandleForApplyQuitMediator:
		log.Info("Enter DepositChaincode Contract " + HandleForApplyQuitMediator + " Invoke")
		return d.handleForApplyQuitMediator(stub, args)
		//  基金会对退出申请Jury进行处理
	case HandleForApplyQuitJury:
		log.Info("Enter DepositChaincode Contract " + HandleForApplyQuitJury + " Invoke")
		return d.handleForApplyQuitJury(stub, args)
		//  基金会对退出申请Developer进行处理
	case HandleForApplyQuitDev:
		log.Info("Enter DepositChaincode Contract " + HandleForApplyQuitDev + " Invoke")
		return d.handleForApplyQuitDev(stub, args)
		//  基金会对申请没收做相应的处理
	case HandleForForfeitureApplication:
		log.Info("Enter DepositChaincode Contract " + HandleForForfeitureApplication + " Invoke")
		return d.handleForForfeitureApplication(stub, args)
	//
	//  申请保证金没收
	case ApplyForForfeitureDeposit:
		log.Info("Enter DepositChaincode Contract " + ApplyForForfeitureDeposit + " Invoke")
		return d.applyForForfeitureDeposit(stub, args)
	//
	//  获取Mediator申请加入列表
	case GetBecomeMediatorApplyList:
		log.Info("Enter DepositChaincode Contract " + GetBecomeMediatorApplyList + " Invoke")
		list, err := stub.GetState(ListForApplyBecomeMediator)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("{}"))
		}
		return shim.Success(list)
		//  查看是否在become列表中
	case IsInBecomeList:
		log.Info("Enter DepositChaincode Contract " + IsInBecomeList + " Invoke")
		list, err := getList(stub, ListForApplyBecomeMediator)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("false"))
		}
		if len(args) != 1 {
			return shim.Error("arg need one")
		}
		if list[args[0]] {
			return shim.Success([]byte("true"))
		}
		return shim.Success([]byte("false"))
		//  获取已同意的mediator列表
	case GetAgreeForBecomeMediatorList:
		log.Info("Enter DepositChaincode Contract " + GetAgreeForBecomeMediatorList + " Invoke")
		list, err := stub.GetState(ListForAgreeBecomeMediator)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("{}"))
		}
		return shim.Success(list)
		//  查看是否在agree列表中
	case modules.IsApproved:
		log.Info("Enter DepositChaincode Contract " + modules.IsApproved + " Invoke")
		list, err := getList(stub, ListForAgreeBecomeMediator)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("false"))
		}
		if len(args) != 1 {
			return shim.Error("arg need one")
		}
		if list[args[0]] {
			return shim.Success([]byte("true"))
		}
		return shim.Success([]byte("false"))
		//获取申请退出列表
	case GetQuitApplyList:
		log.Info("Enter DepositChaincode Contract " + GetQuitApplyList + " Invoke")
		list, err := stub.GetState(ListForQuit)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("{}"))
		}
		return shim.Success(list)
		//  查看是否在退出列表中
	case IsInQuitList:
		log.Info("Enter DepositChaincode Contract " + IsInQuitList + " Invoke")
		list, err := GetListForQuit(stub)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("false"))
		}
		if len(args) != 1 {
			return shim.Error("arg need one")
		}
		if _, ok := list[args[0]]; ok {
			return shim.Success([]byte("true"))
		}
		return shim.Success([]byte("false"))
		//  获取没收保证金申请列表
	case GetListForForfeitureApplication:
		log.Info("Enter DepositChaincode Contract " + GetListForForfeitureApplication + " Invoke")
		list, err := stub.GetState(ListForForfeiture)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("{}"))
		}
		return shim.Success(list)
		//
	case IsInForfeitureList:
		log.Info("Enter DepositChaincode Contract " + IsInForfeitureList + " Invoke")
		list, err := GetListForForfeiture(stub)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("false"))
		}
		if len(args) != 1 {
			return shim.Error("arg need one")
		}
		if _, ok := list[args[0]]; ok {
			return shim.Success([]byte("true"))
		}
		return shim.Success([]byte("false"))

		//  获取Mediator候选列表
	case GetListForMediatorCandidate:
		log.Info("Enter DepositChaincode Contract " + GetListForMediatorCandidate + " Invoke")
		list, err := stub.GetState(modules.MediatorList)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("{}"))
		}
		return shim.Success(list)
		//  查看节点是否在候选列表中
	case IsInMediatorCandidateList:
		log.Info("Enter DepositChaincode Contract " + IsInMediatorCandidateList + " Invoke")
		list, err := getList(stub, modules.MediatorList)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("false"))
		}
		if len(args) != 1 {
			return shim.Error("arg need one")
		}
		if list[args[0]] {
			return shim.Success([]byte("true"))
		}
		return shim.Success([]byte("false"))
		//  获取Jury候选列表
	case GetListForJuryCandidate:
		log.Info("Enter DepositChaincode Contract " + GetListForJuryCandidate + " Invoke")
		list, err := stub.GetState(modules.JuryList)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("{}"))
		}
		return shim.Success(list)
		//  查看jury是否在候选列表中
	case IsInJuryCandidateList:
		log.Info("Enter DepositChaincode Contract " + IsInJuryCandidateList + " Invoke")
		list, err := getList(stub, modules.JuryList)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("false"))
		}
		if len(args) != 1 {
			return shim.Error("arg need one")
		}
		if list[args[0]] {
			return shim.Success([]byte("true"))
		}
		return shim.Success([]byte("false"))
		//  获取Contract Developer候选列表
	case GetListForDeveloper:
		log.Info("Enter DepositChaincode Contract " + GetListForDeveloper + " Invoke")
		list, err := stub.GetState(modules.DeveloperList)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("{}"))
		}
		return shim.Success(list)
		//  查看developer是否在候选列表中
	case IsInDeveloperList:
		log.Info("Enter DepositChaincode Contract " + IsInDeveloperList + " Invoke")
		list, err := getList(stub, modules.DeveloperList)
		if err != nil {
			return shim.Error(err.Error())
		}
		if list == nil {
			return shim.Success([]byte("false"))
		}
		if len(args) != 1 {
			return shim.Error("arg need one")
		}
		if list[args[0]] {
			return shim.Success([]byte("true"))
		}
		return shim.Success([]byte("false"))
		//  获取jury/dev节点的账户
	case GetDeposit:
		log.Info("Enter DepositChaincode Contract " + GetDeposit + " Invoke")
		balance, err := GetNodeBalance(stub, args[0])
		if err != nil {
			return shim.Error(err.Error())
		}
		if balance == nil {
			return shim.Success([]byte("balance is nil"))
		}
		byte, err := json.Marshal(balance)
		if err != nil {
			return shim.Error(err.Error())
		}
		return shim.Success(byte)
		// 获取mediator Deposit
	case modules.GetMediatorDeposit:
		log.Info("Enter DepositChaincode Contract " + modules.GetMediatorDeposit + " Invoke")
		mediator, err := GetMediatorDeposit(stub, args[0])
		if err != nil {
			return shim.Error(err.Error())
		}
		if mediator == nil {
			return shim.Success([]byte("mediator is nil"))
		}
		byte, err := json.Marshal(mediator)
		if err != nil {
			return shim.Error(err.Error())
		}
		return shim.Success(byte)

	//  普通用户质押投票
	case PledgeDeposit:
		log.Info("Enter DepositChaincode Contract " + PledgeDeposit + " Invoke")
		return d.processPledgeDeposit(stub, args)
	case PledgeWithdraw: //提币质押申请（如果提币申请金额为MaxUint64表示全部提现）
		log.Info("Enter DepositChaincode Contract " + PledgeWithdraw + " Invoke")
		return d.processPledgeWithdraw(stub, args)

	case QueryPledgeStatusByAddr: //查询某用户的质押状态
		log.Info("Enter DepositChaincode Contract " + QueryPledgeStatusByAddr + " Query")
		return queryPledgeStatusByAddr(stub, args)
	case QueryAllPledgeHistory: //查询质押分红历史
		log.Info("Enter DepositChaincode Contract " + QueryAllPledgeHistory + " Query")
		return queryAllPledgeHistory(stub, args)

	case HandlePledgeReward: //质押分红处理
		log.Info("Enter DepositChaincode Contract " + HandlePledgeReward + " Invoke")
		return d.handlePledgeReward(stub, args)
	case QueryPledgeList:
		log.Info("Enter DepositChaincode Contract " + QueryPledgeList + " Query")
		return queryPledgeList(stub, args)
		//TODO Devin一个用户，怎么查看自己的流水账？
		//case AllPledgeVotes:
		//	b, err := getVotes(stub)
		//	if err != nil {
		//		return shim.Error(err.Error())
		//	}
		//	st := strconv.FormatInt(b, 10)
		//	return shim.Success([]byte(st))
	case HandleMediatorInCandidateList:
		return d.handleMediatorInCandidateList(stub, args)
	case HandleJuryInCandidateList:
		return d.handleJuryInCandidateList(stub, args)
	case HandleDevInList:
		return d.handleDevInList(stub, args)
	case GetAllMediator:
		values, err := stub.GetStateByPrefix(string(constants.MEDIATOR_INFO_PREFIX) +
			string(constants.DEPOSIT_BALANCE_PREFIX))
		if err != nil {
			log.Debugf("stub.GetStateByPrefix error: %s", err.Error())
			return shim.Error(err.Error())
		}
		if len(values) > 0 {
			mediators := make(map[string]*MediatorDeposit)
			for _, v := range values {
				m := MediatorDeposit{}
				err := json.Unmarshal(v.Value, &m)
				if err != nil {
					log.Debugf("json.Unmarshal error: %s", err.Error())
					return shim.Error(err.Error())
				}
				mediators[v.Key] = &m
			}
			bytes, err := json.Marshal(mediators)
			if err != nil {
				log.Debugf("json.Marshal error: %s", err.Error())
				return shim.Error(err.Error())
			}
			return shim.Success(bytes)
		}
		return shim.Success([]byte("{}"))
	case GetAllNode:
		values, err := stub.GetStateByPrefix(string(constants.DEPOSIT_BALANCE_PREFIX))
		if err != nil {
			log.Debugf("stub.GetStateByPrefix error: %s", err.Error())
			return shim.Error(err.Error())
		}
		if len(values) > 0 {
			node := make(map[string]*DepositBalance)
			for _, v := range values {
				n := DepositBalance{}
				err := json.Unmarshal(v.Value, &n)
				if err != nil {
					log.Debugf("json.Unmarshal error: %s", err.Error())
					return shim.Error(err.Error())
				}
				node[v.Key] = &n
			}
			bytes, err := json.Marshal(node)
			if err != nil {
				log.Debugf("json.Marshal error: %s", err.Error())
				return shim.Error(err.Error())
			}
			return shim.Success(bytes)
		}
		return shim.Success([]byte("{}"))
	}
	return shim.Error("please enter validate function name")
}

func (d *DepositChaincode) applyBecomeMediator(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return applyBecomeMediator(stub, args)
}

func (d *DepositChaincode) mediatorPayToDepositContract(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return mediatorPayToDepositContract(stub/*, args*/)
}

func (d *DepositChaincode) mediatorApplyQuit(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return mediatorApplyQuit(stub/*, args*/)
}

func (d *DepositChaincode) UpdateMediatorInfo(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return updateMediatorInfo(stub, args)
}

//

func (d *DepositChaincode) juryPayToDepositContract(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return juryPayToDepositContract(stub, args)
}
func (d *DepositChaincode) juryApplyQuit(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return juryApplyQuit(stub, args)
}

//

func (d *DepositChaincode) developerPayToDepositContract(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return developerPayToDepositContract(stub, args)
}
func (d *DepositChaincode) devApplyQuit(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return devApplyQuit(stub, args)
}

//

//基金会对申请加入Mediator进行处理
func (d *DepositChaincode) handleForApplyBecomeMediator(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleForApplyBecomeMediator(stub, args)
}

//基金会对申请退出Mediator进行处理
func (d *DepositChaincode) handleForApplyQuitMediator(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleForApplyQuitMediator(stub, args)
}

func (d *DepositChaincode) handleForApplyQuitJury(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleForApplyQuitJury(stub, args)
}

func (d *DepositChaincode) handleForApplyQuitDev(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleForApplyQuitDev(stub, args)
}

func (d *DepositChaincode) handleForForfeitureApplication(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleForForfeitureApplication(stub, args)
}

func (d DepositChaincode) handleNodeRemoveFromAgreeList(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return hanldeNodeRemoveFromAgreeList(stub, args)
}

//

func (d DepositChaincode) applyForForfeitureDeposit(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return applyForForfeitureDeposit(stub, args)
}

//  质押

func (d DepositChaincode) processPledgeDeposit(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return processPledgeDeposit(stub, args)
}

func (d DepositChaincode) processPledgeWithdraw(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return processPledgeWithdraw(stub, args)
}

func (d DepositChaincode) handlePledgeReward(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handlePledgeReward(stub, args)
}

func (d DepositChaincode) handleMediatorInCandidateList(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleNodeInList(stub, args, Mediator)
}
func (d DepositChaincode) handleJuryInCandidateList(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleNodeInList(stub, args, Jury)
}
func (d DepositChaincode) handleDevInList(stub shim.ChaincodeStubInterface, args []string) pb.Response {
	return handleNodeInList(stub, args, Developer)
}

//
//func (d DepositChaincode) handleRemoveMediatorNode(stub shim.ChaincodeStubInterface, args []string) pb.Response {
//	return handleRemoveMediatorNode(stub, args)
//}
//
////
//func (d DepositChaincode) handleRemoveNormalNode(stub shim.ChaincodeStubInterface, args []string) pb.Response {
//	return handleRemoveNormalNode(stub, args)
//}
