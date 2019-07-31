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
 *  * @author PalletOne core developer Albert·Gou <dev@pallet.one>
 *  * @date 2018
 *
 */

package modules

import (
	"github.com/palletone/go-palletone/core"
)

const (
	ApplyMediator      = "ApplyBecomeMediator"
	IsApproved         = "IsInAgreeList"
	MediatorPayDeposit = "MediatorPayToDepositContract"
	MediatorList       = "MediatorList"
	GetMediatorDeposit = "GetMediatorDeposit"
	MediatorApplyQuit  = "MediatorApplyQuit"
	UpdateMediatorInfo = "UpdateMediatorInfo"
)

type MediatorInfo struct {
	*core.MediatorInfoBase
	*core.MediatorApplyInfo
	*core.MediatorInfoExpand
}

func NewMediatorInfo() *MediatorInfo {
	return &MediatorInfo{
		MediatorInfoBase:   core.NewMediatorInfoBase(),
		MediatorApplyInfo:  core.NewMediatorApplyInfo(),
		MediatorInfoExpand: core.NewMediatorInfoExpand(),
	}
}

func MediatorToInfo(md *core.Mediator) *MediatorInfo {
	mi := NewMediatorInfo()
	mi.AddStr = md.Address.Str()
	mi.InitPubKey = core.PointToStr(md.InitPubKey)
	mi.Node = md.Node.String()
	*mi.MediatorApplyInfo = *md.MediatorApplyInfo
	*mi.MediatorInfoExpand = *md.MediatorInfoExpand

	return mi
}

func (mi *MediatorInfo) InfoToMediator() *core.Mediator {
	md := core.NewMediator()
	md.Address, _ = core.StrToMedAdd(mi.AddStr)
	md.InitPubKey, _ = core.StrToPoint(mi.InitPubKey)
	md.Node, _ = core.StrToMedNode(mi.Node)
	*md.MediatorApplyInfo = *mi.MediatorApplyInfo
	*md.MediatorInfoExpand = *mi.MediatorInfoExpand

	return md
}

type MediatorCreateOperation struct {
	*core.MediatorInfoBase
	*core.MediatorApplyInfo
}

func NewMediatorCreateOperation() *MediatorCreateOperation {
	return &MediatorCreateOperation{
		MediatorInfoBase:  core.NewMediatorInfoBase(),
		MediatorApplyInfo: core.NewMediatorApplyInfo(),
	}
}

// 更新 mediator 信息所需参数
type MediatorUpdateArgs struct {
	AddStr      string  `json:"account"` // 账户地址
	Logo        *string `json:"logo"`    // 节点图标url
	Name        *string `json:"name"`    // 节点名称
	Location    *string `json:"loc"`     // 节点所在地区
	Url         *string `json:"url"`     // 节点网站
	Description *string `json:"desc"`    // 节点信息描述
	Node        *string `json:"node"`    // 节点网络信息，包括ip和端口等
}
