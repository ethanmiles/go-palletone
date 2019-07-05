// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package crypto

import (
	"bytes"
	"crypto/ecdsa"
	"testing"

	"github.com/palletone/go-palletone/common"
	"github.com/palletone/go-palletone/common/hexutil"
	"github.com/palletone/go-palletone/common/math"
	"github.com/btcsuite/btcd/btcec"
	"encoding/hex"
	"github.com/stretchr/testify/assert"
)

var (
	testmsg     = hexutil.MustDecode("0xce0677bb30baa8cf067c88db9811f4333d131bf8bcf12fe7065d211dce971008")
	testsig     = hexutil.MustDecode("0x90f27b8b488db00b00606796d2987f6a5f59ae62ea05effe84fef5b8b0e549984a691139ad57a3f0b906637673aa2f63d1f55cb1a69199d4009eea23ceaddc9301")
	testpubkey  = hexutil.MustDecode("0x04e32df42865e97135acfb65f3bae71bdc86f4d49150ad6a440b6f15878109880a0a2b2667f7e725ceea70c673093bf67663e0312623c8e091b13cf2c0f11ef652")
	testpubkeyc = hexutil.MustDecode("0x02e32df42865e97135acfb65f3bae71bdc86f4d49150ad6a440b6f15878109880a")
)

/*
func TestEcrecover(t *testing.T) {
	pubkey, err := Ecrecover(testmsg, testsig)
	if err != nil {
		t.Fatalf("recover error: %s", err)
	}
	if !bytes.Equal(pubkey, testpubkey) {
		t.Errorf("pubkey mismatch: want: %x have: %x", testpubkey, pubkey)
	}
}
*/
func TestVerifySignature(t *testing.T) {
	sig := testsig[:len(testsig)-1] // remove recovery id
	if pass,_:=MyCryptoLib.Verify(testpubkey, sig,testmsg);!pass {
		t.Errorf("can't verify signature with uncompressed key")
	}
	if pass,_:=MyCryptoLib.Verify(testpubkeyc, sig,testmsg);!pass {
		t.Errorf("can't verify signature with compressed key")
	}

	if pass,_:=MyCryptoLib.Verify(nil, sig,testmsg);pass {
		t.Errorf("signature valid with no key")
	}
	if pass,_:=MyCryptoLib.Verify(testpubkey, sig, nil);pass {
		t.Errorf("signature valid with no message")
	}
	if pass,_:=MyCryptoLib.Verify(testpubkey, nil,testmsg);pass {
		t.Errorf("nil signature valid")
	}
	if pass,_:=MyCryptoLib.Verify(testpubkey, append(common.CopyBytes(sig), 1, 2, 3), testmsg);pass {
		t.Errorf("signature valid with extra bytes at the end")
	}
	if pass,_:=MyCryptoLib.Verify(testpubkey, sig,testmsg[:len(testmsg)-2]);pass {
		t.Errorf("signature valid even though it's incomplete")
	}
	wrongkey := common.CopyBytes(testpubkey)
	wrongkey[10]++
	if pass,_:=MyCryptoLib.Verify(wrongkey, sig,testmsg);pass {
		t.Errorf("signature valid with with wrong public key")
	}
}

// This test checks that VerifySignature rejects malleable signatures with s > N/2.
func TestVerifySignatureMalleable(t *testing.T) {
	sig := hexutil.MustDecode("0x638a54215d80a6713c8d523a6adc4e6e73652d859103a36b700851cb0e61b66b8ebfc1a610c57d732ec6e0a8f06a9a7a28df5051ece514702ff9cdff0b11f454")
	key := hexutil.MustDecode("0x03ca634cae0d49acb401d8a4c6b6fe8c55b70d115bf400769cc1400f3258cd3138")
	msg := hexutil.MustDecode("0xd301ce462d3e639518f482c7f03821fec1e602018630ce621e1e7851c12343a6")
	if pass,_:=MyCryptoLib.Verify(key, sig,msg);pass {
		t.Error("VerifySignature returned true for malleable signature")
	}
}

func TestDecompressPubkey(t *testing.T) {
	key, err := DecompressPubkey(testpubkeyc)
	if err != nil {
		t.Fatal(err)
	}
	if uncompressed := FromECDSAPub(key); !bytes.Equal(uncompressed, testpubkey) {
		t.Errorf("wrong public key result: got %x, want %x", uncompressed, testpubkey)
	}
	if _, err := DecompressPubkey(nil); err == nil {
		t.Errorf("no error for nil pubkey")
	}
	if _, err := DecompressPubkey(testpubkeyc[:5]); err == nil {
		t.Errorf("no error for incomplete pubkey")
	}
	if _, err := DecompressPubkey(append(common.CopyBytes(testpubkeyc), 1, 2, 3)); err == nil {
		t.Errorf("no error for pubkey with extra bytes at the end")
	}
}

func TestCompressPubkey(t *testing.T) {
	key := &ecdsa.PublicKey{
		Curve: btcec.S256(),
		X:     math.MustParseBig256("0xe32df42865e97135acfb65f3bae71bdc86f4d49150ad6a440b6f15878109880a"),
		Y:     math.MustParseBig256("0x0a2b2667f7e725ceea70c673093bf67663e0312623c8e091b13cf2c0f11ef652"),
	}
	compressed := compressPubkey(key)
	if !bytes.Equal(compressed, testpubkeyc) {
		t.Errorf("wrong public key result: got %x, want %x", compressed, testpubkeyc)
	}
}

func TestPubkeyRandom(t *testing.T) {
	const runs = 200

	for i := 0; i < runs; i++ {
		key, err := MyCryptoLib.KeyGen()
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		pubkey2, err :=MyCryptoLib.PrivateKeyToPubKey(key)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		t.Logf("PubKey:%x",pubkey2)
		//if !reflect.DeepEqual(key.PublicKey, *pubkey2) {
		//	t.Fatalf("iteration %d: keys not equal", i)
		//}
	}
}

/*
func BenchmarkEcrecoverSignature(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := Ecrecover(testmsg, testsig); err != nil {
			b.Fatal("ecrecover error", err)
		}
	}
}
*/
func BenchmarkVerifySignature(b *testing.B) {
	sig := testsig[:len(testsig)-1] // remove recovery id
	for i := 0; i < b.N; i++ {
		if pass,_:=MyCryptoLib.Verify(testpubkey, sig,testmsg);!pass {
			b.Fatal("verify error")
		}
	}
}

func BenchmarkDecompressPubkey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := DecompressPubkey(testpubkeyc); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSignVerify(t *testing.T) {
	//sign := "0xda850b649658b2863559c338fdd99858acc20884f1b1f097d09735346b059b111e6c0f46661d45987e60a81fc4329b357c609dde1f5da187cdbeb5a57cc61f8d01"
	text := "a"
	hash := Keccak256([]byte(text))

	privateKey := "f4b430cd1007bf3309a00fdda81c58131a1e0a41f6a72eab3291e561342ae1b3"
	// privateKeyBytes := hexutil.MustDecode(privateKey)
	prvKey, _ := hex.DecodeString(privateKey)

	//signB, _ := hexutil.Decode(sign)
	signature, err := MyCryptoLib.Sign(prvKey,hash)
	assert.Nil(t,err)
	t.Log("Signature is: " + hexutil.Encode(signature))
	t.Logf("Sign len:%d", len(signature))
	pubKey,_ := MyCryptoLib.PrivateKeyToPubKey(prvKey)
	pass ,err:=MyCryptoLib.Verify(pubKey,  signature,hash)
	assert.Nil(t,err)
	if pass {
		t.Log("Pass")
	} else {
		t.Error("No Pass")
	}
}
