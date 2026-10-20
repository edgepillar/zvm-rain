// Package keys is the operator's secrets file: one key, the bot's, which is
// Rain's operator.
//
// It may not be derived from anything public, unlike the rest of zvm's devnet
// keys: the operator chooses who a rain pays, so whoever holds it can rain any
// linked sender's deposit and allowance onto addresses they linked themselves.
package keys

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type File struct {
	Bot string `json:"bot"`
}

type Keys struct {
	Bot *ecdsa.PrivateKey
}

func (k *Keys) BotAddress() common.Address { return crypto.PubkeyToAddress(k.Bot.PublicKey) }

// Generate writes a new secrets file and refuses to replace one: the key is
// the contract's operator, and losing it means deploying a new contract that
// everyone links to again.
func Generate(path string) (*Keys, error) {
	k, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(File{Bot: hex.EncodeToString(crypto.FromECDSA(k))}, "", "  ")
	if err != nil {
		return nil, err
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := out.Write(append(raw, '\n')); err != nil {
		out.Close()
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	return Load(path)
}

func Load(path string) (*Keys, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Bot == "" {
		return nil, fmt.Errorf("%s: no bot key", path)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(f.Bot, "0x"))
	if err != nil {
		return nil, fmt.Errorf("%s: bot key: %w", path, err)
	}
	return &Keys{Bot: key}, nil
}

// DevEVMKey is zvm's deterministic devnet key, keccak256("zvm-dev-evm-<i>"),
// the same derivation as zvm's devnet.EVMKey. Index 900 is the devnet
// deployer, which holds the ZNN that funds the bot's gas. Public, so it only
// ever pays; it never operates anything.
func DevEVMKey(i uint32) *ecdsa.PrivateKey {
	key, err := crypto.ToECDSA(crypto.Keccak256([]byte(fmt.Sprintf("zvm-dev-evm-%d", i))))
	if err != nil {
		panic(err)
	}
	return key
}
