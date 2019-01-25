/*
 *
 *    This file is part of go-palletone.
 *    go-palletone is free software: you can redistribute it and/or modify
 *    it under the terms of the GNU General Public License as published by
 *    the Free Software Foundation, either version 3 of the License, or
 *    (at your option) any later version.
 *    go-palletone is distributed in the hope that it will be useful,
 *    but WITHOUT ANY WARRANTY; without even the implied warranty of
 *    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *    GNU General Public License for more details.
 *    You should have received a copy of the GNU General Public License
 *    along with go-palletone.  If not, see <http://www.gnu.org/licenses/>.
 * /
 *
 *  * @author PalletOne core developer <dev@pallet.one>
 *  * @date 2018-2019
 *
 */

package validator

import (
	"github.com/palletone/go-palletone/common/log"
	"github.com/palletone/go-palletone/dag/modules"
	"github.com/palletone/go-palletone/dag/vote"
)

/**
验证某个交易，tx具有以下规则：
Tx的第一条Msg必须是Payment
如果有ContractInvokeRequest，那么要么：
	1.	ContractInvoke不存在（这是一个Request）
	2.	ContractInvoke必然在Request的下面，不可能在Request的上面
To validate one transaction
*/
func (validate *Validate) validateTx(tx *modules.Transaction, isCoinbase bool) ValidationCode {
	if len(tx.TxMessages) == 0 {
		return TxValidationCode_INVALID_MSG
	}

	if tx.TxMessages[0].App != modules.APP_PAYMENT { // 交易费
		//fmt.Printf("-----------ValidateTx , %d\n", tx.TxMessages[0].App)
		return TxValidationCode_INVALID_MSG
	}

	if validate.checkTxIsExist(tx) {
		return TxValidationCode_DUPLICATE_TXID
	}
	if !validate.validateTxFee(tx) {
		return TxValidationCode_INVALID_FEE
	}

	// validate transaction hash
	//if !bytes.Equal(tx.TxHash.Bytes(), tx.Hash().Bytes()) {
	//	return TxValidationCode_NIL_TXACTION
	//}

	for msgIdx, msg := range tx.TxMessages {
		// check message type and payload
		if !validateMessageType(msg.App, msg.Payload) {
			return TxValidationCode_UNKNOWN_TX_TYPE
		}
		// validate tx size
		if tx.Size().Float64() > float64(modules.TX_MAXSIZE) {
			log.Debug("Tx size is to big.")
			return TxValidationCode_NOT_COMPARE_SIZE
		}

		// validate transaction signature
		if validateTxSignature(tx) == false {
			return TxValidationCode_BAD_CREATOR_SIGNATURE
		}
		// validate every type payload
		switch msg.App {
		case modules.APP_PAYMENT:
			payment, ok := msg.Payload.(*modules.PaymentPayload)
			if !ok {
				return TxValidationCode_INVALID_PAYMMENTLOAD
			}
			validateCode := validate.validatePaymentPayload(tx,msgIdx, payment, isCoinbase)
			if validateCode != TxValidationCode_VALID {
				return validateCode
			}
		case modules.APP_CONTRACT_TPL:
			payload, _ := msg.Payload.(*modules.ContractTplPayload)
			validateCode := validate.validateContractTplPayload(payload)
			if validateCode != TxValidationCode_VALID {
				return validateCode
			}
		case modules.APP_CONTRACT_DEPLOY:
			payload, _ := msg.Payload.(*modules.ContractDeployPayload)
			validateCode := validate.validateContractState(payload.ContractId, &payload.ReadSet, &payload.WriteSet)
			if validateCode != TxValidationCode_VALID {
				return validateCode
			}
		case modules.APP_CONTRACT_INVOKE:
			payload, _ := msg.Payload.(*modules.ContractInvokePayload)
			validateCode := validate.validateContractState(payload.ContractId, &payload.ReadSet, &payload.WriteSet)
			if validateCode != TxValidationCode_VALID {
				return validateCode
			}
		case modules.APP_CONTRACT_TPL_REQUEST:
			payload, _ := msg.Payload.(*modules.ContractInstallRequestPayload)
			if payload.TplName == "" || payload.Path == "" || payload.Version == "" {
				return TxValidationCode_INVALID_CONTRACT
			}
			return TxValidationCode_VALID

		case modules.APP_CONTRACT_DEPLOY_REQUEST:
			// 参数临界值验证
			payload, _ := msg.Payload.(*modules.ContractDeployRequestPayload)
			if len(payload.TplId) == 0 || payload.TxId == "" || payload.Timeout < 0 {
				return TxValidationCode_INVALID_CONTRACT
			}

			validateCode := validate.validateContractdeploy(payload.TplId)
			return validateCode

		case modules.APP_CONTRACT_INVOKE_REQUEST:

			payload, _ := msg.Payload.(*modules.ContractInvokeRequestPayload)
			if len(payload.ContractId) == 0 {
				return TxValidationCode_INVALID_CONTRACT
			}
			// 验证ContractId有效性
			if len(payload.ContractId) <= 0 {
				return TxValidationCode_INVALID_CONTRACT
			}
			return TxValidationCode_VALID

		case modules.APP_SIGNATURE:
			// 签名验证
			payload, _ := msg.Payload.(*modules.SignaturePayload)
			validateCode := validate.validateContractSignature(payload.Signatures[:], tx)
			if validateCode != TxValidationCode_VALID {
				return validateCode
			}

		case modules.APP_CONFIG:
		case modules.APP_DATA:
			payload, _ := msg.Payload.(*modules.DataPayload)
			validateCode := validate.validateDataPayload(payload)
			if validateCode != TxValidationCode_VALID {
				return validateCode
			}
		case modules.APP_VOTE:
		case modules.OP_MEDIATOR_CREATE:
		default:
			return TxValidationCode_UNKNOWN_TX_TYPE
		}
	}
	return TxValidationCode_VALID
}

func (validate *Validate) validateTxFee(tx *modules.Transaction) bool {
	if validate.utxoquery == nil {
		log.Warnf("Cannot validate tx fee, your validate utxoquery not set")
		return true
	}
	fee, err := tx.GetTxFee(validate.utxoquery.GetUtxoEntry)
	if err != nil {
		log.Error("compute tx fee error", err.Error())
		return false
	}
	minFee := &modules.AmountAsset{0, modules.NewPTNAsset()}
	if validate.statequery != nil {
		minFee, err = validate.statequery.GetMinFee()
	}
	if minFee.Amount > 0 { //需要验证最小手续费
		if fee.Asset.String() != minFee.Asset.String() || fee.Amount < minFee.Amount {
			return false
		}
	}
	return true
}

/**
检查message的app与payload是否一致
check messaage 'app' consistent with payload type
*/
func validateMessageType(app modules.MessageType, payload interface{}) bool {
	switch t := payload.(type) {
	case *modules.PaymentPayload:
		if app == modules.APP_PAYMENT {
			return true
		}
	case *modules.ContractTplPayload:
		if app == modules.APP_CONTRACT_TPL {
			return true
		}
	case *modules.ContractDeployPayload:
		if app == modules.APP_CONTRACT_DEPLOY {
			return true
		}
	case *modules.ContractInvokeRequestPayload:
		if app == modules.APP_CONTRACT_INVOKE_REQUEST {
			return true
		}
	case *modules.ContractInvokePayload:
		if app == modules.APP_CONTRACT_INVOKE {
			return true
		}
	case *modules.SignaturePayload:
		if app == modules.APP_SIGNATURE {
			return true
		}

	case *modules.ConfigPayload:
		if app == modules.APP_CONFIG {
			return true
		}
	case *modules.DataPayload:
		if app == modules.APP_DATA {
			return true
		}
	case *vote.VoteInfo:
		if app == modules.APP_VOTE {
			return true
		}
	case *modules.MediatorCreateOperation:
		if app == modules.OP_MEDIATOR_CREATE {
			return true
		}
	case *modules.ContractDeployRequestPayload:
		if app == modules.APP_CONTRACT_DEPLOY_REQUEST {
			return true
		}
	case *modules.ContractInstallRequestPayload:
		if app == modules.APP_CONTRACT_TPL_REQUEST {
			return true
		}

	default:
		log.Debug("The payload of message type is unexpected. ", "payload_type", t, "app type", app)
		return false
	}
	return false
}
