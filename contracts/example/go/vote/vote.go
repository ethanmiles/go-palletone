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
 * Copyright IBM Corp. All Rights Reserved.
 * @author PalletOne core developers <dev@pallet.one>
 * @date 2018
 */

package vote

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/palletone/go-palletone/common"
	"github.com/palletone/go-palletone/contracts/shim"
	pb "github.com/palletone/go-palletone/core/vmContractPub/protos/peer"
	dm "github.com/palletone/go-palletone/dag/modules"
)

const symbolsKey = "symbols"

type Vote struct {
}

//one topic
type VoteTopic struct {
	TopicTitle    string
	SelectOptions []string
	SelectMax     uint64
}
type VoteResult struct {
	SelectOption string
	Num          uint64
}

//topic support result
type TopicSupports struct {
	TopicTitle  string
	VoteResults []VoteResult
	SelectMax   uint64
	//SelectOptionsNum  uint64
}

//vote token information
type TokenInfo struct {
	Name        string
	Symbol      string
	CreateAddr  string
	VoteType    byte
	TotalSupply uint64
	VoteEndTime time.Time
	VoteContent []byte
	AssetID     dm.IDType16
}

//one user's support
type SupportRequest struct {
	TopicIndex   uint64
	SelectIndexs []uint64
}

type Symbols struct {
	TokenInfos map[string]TokenInfo `json:"tokeninfos"`
}

func (v *Vote) Init(stub shim.ChaincodeStubInterface) pb.Response {
	return shim.Success(nil)
}

func (v *Vote) Invoke(stub shim.ChaincodeStubInterface) pb.Response {
	f, args := stub.GetFunctionAndParameters()

	switch f {
	case "createToken":
		return createToken(args, stub)
	case "support":
		return support(args, stub)
	case "getVoteResult":
		return getVoteResult(args, stub)
	default:
		jsonResp := "{\"Error\":\"Unknown function " + f + "\"}"
		return shim.Error(jsonResp)
	}
}

func setSymbols(symbols *Symbols, stub shim.ChaincodeStubInterface) error {
	val, err := json.Marshal(symbols)
	if err != nil {
		return err
	}
	err = stub.PutState(symbolsKey, val)
	return err
}

func getSymbols(stub shim.ChaincodeStubInterface) (*Symbols, error) {
	//
	symbols := Symbols{TokenInfos: map[string]TokenInfo{}}
	symbolsBytes, err := stub.GetState(symbolsKey)
	if err != nil {
		return &symbols, err
	}
	//
	err = json.Unmarshal(symbolsBytes, &symbols)
	if err != nil {
		return &symbols, err
	}

	return &symbols, nil
}

func createToken(args []string, stub shim.ChaincodeStubInterface) pb.Response {
	//params check
	if len(args) < 5 {
		return shim.Error("need 5 args (Name,VoteType,TotalSupply,VoteEndTime,VoteContentJson)")
	}

	//==== convert params to token information
	var vt dm.VoteToken
	//name symbol
	vt.Name = args[0]
	vt.Symbol = "VOTE"

	//vote type
	if args[1] == "0" {
		vt.VoteType = byte(0)
	} else if args[1] == "1" {
		vt.VoteType = byte(1)
	} else if args[1] == "2" {
		vt.VoteType = byte(2)
	} else {
		jsonResp := "{\"Error\":\"Only string, 0 or 1 or 2\"}"
		return shim.Success([]byte(jsonResp))
	}
	//total supply
	totalSupply, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to convert total supply\"}"
		return shim.Error(jsonResp)
	}
	if totalSupply == 0 {
		jsonResp := "{\"Error\":\"Can't be zero\"}"
		return shim.Success([]byte(jsonResp))
	}
	vt.TotalSupply = totalSupply
	//VoteEndTime
	VoteEndTime, err := time.Parse("2006-01-02 15:04:05", args[3])
	if err != nil {
		jsonResp := "{\"Error\":\"No vote end time\"}"
		return shim.Success([]byte(jsonResp))
	}
	vt.VoteEndTime = VoteEndTime
	//VoteContent
	var voteTopics []VoteTopic
	err = json.Unmarshal([]byte(args[4]), &voteTopics)
	if err != nil {
		jsonResp := "{\"Error\":\"VoteContent format invalid\"}"
		return shim.Success([]byte(jsonResp))
	}
	//init support
	var supports []TopicSupports
	for _, oneTopic := range voteTopics {
		var oneSupport TopicSupports
		oneSupport.TopicTitle = oneTopic.TopicTitle
		for _, oneOption := range oneTopic.SelectOptions {
			var oneResult VoteResult
			oneResult.SelectOption = oneOption
			oneSupport.VoteResults = append(oneSupport.VoteResults, oneResult)
		}
		//oneResult.SelectOptionsNum = uint64(len(oneRequest.SelectOptions))
		oneSupport.SelectMax = oneTopic.SelectMax
		supports = append(supports, oneSupport)
	}
	voteContentJson, err := json.Marshal(supports)
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to generate voteContent Json\"}"
		return shim.Error(jsonResp)
	}
	vt.VoteContent = voteContentJson

	txid := stub.GetTxID()
	assetID, _ := dm.NewAssetId(vt.Symbol, dm.AssetType_VoteToken,
		0, common.Hex2Bytes(txid[2:]))
	assetIDStr := assetID.ToAssetId()
	//check name is only or not
	symbols, err := getSymbols(stub)
	if _, ok := symbols.TokenInfos[assetIDStr]; ok {
		jsonResp := "{\"Error\":\"Repeat AssetID\"}"
		return shim.Error(jsonResp)
	}

	//convert to json
	createJson, err := json.Marshal(vt)
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to generate token Json\"}"
		return shim.Error(jsonResp)
	}
	//get creator
	createAddr, err := stub.GetInvokeAddress()
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to get invoke address\"}"
		return shim.Error(jsonResp)
	}

	//set token define
	err = stub.DefineToken(byte(dm.AssetType_VoteToken), createJson, createAddr)
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to call stub.DefineToken\"}"
		return shim.Error(jsonResp)
	}

	//last put state
	info := TokenInfo{vt.Name, vt.Symbol, createAddr, vt.VoteType, totalSupply,
		VoteEndTime, voteContentJson, assetID}
	symbols.TokenInfos[assetIDStr] = info

	err = setSymbols(symbols, stub)
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to set symbols\"}"
		return shim.Error(jsonResp)
	}
	return shim.Success(createJson) //test
}

func support(args []string, stub shim.ChaincodeStubInterface) pb.Response {
	//params check
	if len(args) < 1 {
		return shim.Error("need 1 args (SupportRequestJson)")
	}

	//check token
	invokeTokens, err := stub.GetInvokeTokens()
	if err != nil {
		jsonResp := "{\"Error\":\"GetInvokeTokens failed\"}"
		return shim.Success([]byte(jsonResp))
	}
	voteNum := uint64(0)
	assetIDStr := ""
	for i := 0; i < len(invokeTokens); i++ {
		if invokeTokens[i].Asset.AssetId == dm.PTNCOIN {
			continue
		} else if invokeTokens[i].Address == "P1111111111111111111114oLvT2" {
			if assetIDStr == "" {
				assetIDStr = invokeTokens[i].Asset.String()
				voteNum += invokeTokens[i].Amount
			} else if invokeTokens[i].Asset.AssetId.Str() == assetIDStr {
				voteNum += invokeTokens[i].Amount
			}
		}
	}
	if voteNum == 0 || assetIDStr == "" { //no vote token
		jsonResp := "{\"Error\":\"Vote token empty\"}"
		return shim.Success([]byte(jsonResp))
	}

	//check name is exist or not
	symbols, err := getSymbols(stub)
	if _, ok := symbols.TokenInfos[assetIDStr]; !ok {
		jsonResp := "{\"Error\":\"Token not exist\"}"
		return shim.Success([]byte(jsonResp))
	}

	//parse support requests
	var supportRequests []SupportRequest
	err = json.Unmarshal([]byte(args[0]), &supportRequests)
	if err != nil {
		jsonResp := "{\"Error\":\"SupportRequestJson format invalid\"}"
		return shim.Success([]byte(jsonResp))
	}
	//get token information
	tokenInfo := symbols.TokenInfos[assetIDStr]
	var topicSupports []TopicSupports
	err = json.Unmarshal(tokenInfo.VoteContent, &topicSupports)
	if err != nil {
		jsonResp := "{\"Error\":\"Results format invalid, Error!!!\"}"
		return shim.Success([]byte(jsonResp))
	}

	if voteNum < uint64(len(supportRequests)) { //vote token more than request
		jsonResp := "{\"Error\":\"Vote token more than support request\"}"
		return shim.Success([]byte(jsonResp))
	}

	//check time
	headerTime, err := stub.GetTxTimestamp(10)
	if err != nil {
		jsonResp := "{\"Error\":\"GetTxTimestamp invalid, Error!!!\"}"
		return shim.Success([]byte(jsonResp))
	}
	if headerTime.Seconds > tokenInfo.VoteEndTime.Unix() {
		jsonResp := "{\"Error\":\"Vote is over\"}"
		return shim.Success([]byte(jsonResp))
	}

	//save support
	indexHistory := make(map[uint64]uint8)
	indexRepeat := false
	for _, oneSupport := range supportRequests {
		selectIndex := oneSupport.TopicIndex - 1
		if _, ok := indexHistory[selectIndex]; ok { //check select repeat
			indexRepeat = true
			break
		}
		indexHistory[selectIndex] = 1
		if selectIndex < uint64(len(topicSupports)) { //1.check index, must not out of total
			if uint64(len(oneSupport.SelectIndexs)) <= topicSupports[selectIndex].SelectMax { //2.check one select's options, must not out of select's max
				lenOfVoteResult := uint64(len(topicSupports[selectIndex].VoteResults))
				selIndexHistory := make(map[uint64]uint8)
				for _, index := range oneSupport.SelectIndexs {
					if _, ok := selIndexHistory[index]; ok { //check select repeat
						break
					}
					selIndexHistory[index] = 1
					if index > 0 && index < lenOfVoteResult { //3.index must be real select options
						topicSupports[selectIndex].VoteResults[index-1].Num += 1
					}
				}
			}
		}
	}
	if indexRepeat {
		jsonResp := "{\"Error\":\"Repeat index of select option \"}"
		return shim.Error(jsonResp)
	}
	voteContentJson, err := json.Marshal(topicSupports)
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to generate voteContent Json\"}"
		return shim.Error(jsonResp)
	}
	tokenInfo.VoteContent = voteContentJson

	//save token information
	symbols.TokenInfos[assetIDStr] = tokenInfo
	err = setSymbols(symbols, stub)
	if err != nil {
		jsonResp := "{\"Error\":\"Failed to set symbols\"}"
		return shim.Error(jsonResp)
	}
	return shim.Success([]byte("")) //test
}

type TokenIDInfo struct {
	CreateAddr     string
	TotalSupply    uint64
	SupportResults []SupportResult
	AssetID        string
}
type SupportResult struct {
	TopicIndex  uint64
	TopicTitle  string
	VoteResults []VoteResult
}

// A slice of TopicResult that implements sort.Interface to sort by Value.
type VoteResultList []VoteResult

func (p VoteResultList) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p VoteResultList) Len() int           { return len(p) }
func (p VoteResultList) Less(i, j int) bool { return p[i].Num > p[j].Num }

// A function to turn a map into a TopicResultList, then sort and return it.
func sortSupportByCount(tpl VoteResultList) VoteResultList {
	sort.Stable(tpl) //sort.Sort(tpl)
	return tpl
}
func getVoteResult(args []string, stub shim.ChaincodeStubInterface) pb.Response {
	//params check
	if len(args) < 1 {
		return shim.Error("need 1 args (AssetID String)")
	}

	//assetIDStr
	assetIDStr := strings.ToUpper(args[0])
	//check name is exist or not
	symbols, err := getSymbols(stub)
	if _, ok := symbols.TokenInfos[assetIDStr]; !ok {
		jsonResp := "{\"Error\":\"Token not exist\"}"
		return shim.Success([]byte(jsonResp))
	}

	//get token information
	tokenInfo := symbols.TokenInfos[assetIDStr]
	var topicSupports []TopicSupports
	err = json.Unmarshal(tokenInfo.VoteContent, &topicSupports)
	if err != nil {
		jsonResp := "{\"Error\":\"Results format invalid, Error!!!\"}"
		return shim.Success([]byte(jsonResp))
	}

	//calculate result
	var supportResults []SupportResult
	for i, oneTopicSupport := range topicSupports {
		var oneResult SupportResult
		oneResult.TopicIndex = uint64(i) + 1
		oneResult.TopicTitle = oneTopicSupport.TopicTitle
		oneResultSort := sortSupportByCount(oneTopicSupport.VoteResults)
		for i := uint64(0); i < oneTopicSupport.SelectMax; i++ {
			oneResult.VoteResults = append(oneResult.VoteResults, oneResultSort[i])
		}
		supportResults = append(supportResults, oneResult)
	}

	//token
	asset := symbols.TokenInfos[assetIDStr].AssetID
	tkID := TokenIDInfo{CreateAddr: symbols.TokenInfos[assetIDStr].CreateAddr,
		TotalSupply: symbols.TokenInfos[assetIDStr].TotalSupply, SupportResults: supportResults, AssetID: asset.ToAssetId()}

	//return json
	tkJson, err := json.Marshal(tkID)
	if err != nil {
		return shim.Success([]byte(err.Error()))
	}
	return shim.Success(tkJson) //test
}
