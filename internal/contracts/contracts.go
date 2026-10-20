// Package contracts holds Rain as compiled by contracts/scripts/build.mjs, and
// the slice of ERC-20 the bot reads from the tokens it rains.
package contracts

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

//go:embed artifacts/Rain.json
var rainJSON []byte

// Artifact is one build output.
type Artifact struct {
	ContractName      string          `json:"contractName"`
	SourceName        string          `json:"sourceName"`
	CompilerVersion   string          `json:"compilerVersion"`
	ABIJSON           json.RawMessage `json:"abi"`
	Bytecode          hexutil.Bytes   `json:"bytecode"`
	DeployedBytecode  hexutil.Bytes   `json:"deployedBytecode"`
	StandardJSONInput json.RawMessage `json:"standardJsonInput"`

	ABI abi.ABI `json:"-"`
}

func load(raw []byte) *Artifact {
	var a Artifact
	if err := json.Unmarshal(raw, &a); err != nil {
		panic(err)
	}
	var err error
	if a.ABI, err = abi.JSON(strings.NewReader(string(a.ABIJSON))); err != nil {
		panic(fmt.Errorf("%s abi: %w", a.ContractName, err))
	}
	return &a
}

var Rain = load(rainJSON)

// ERC20 is what the bot reads from a token, and approve and WETH9's deposit,
// which the live tests use to fund a sender.
var ERC20 = func() *Artifact {
	a := &Artifact{ContractName: "ERC20"}
	var err error
	a.ABI, err = abi.JSON(strings.NewReader(`[
	 {"type":"function","name":"symbol","stateMutability":"view","inputs":[],"outputs":[{"type":"string"}]},
	 {"type":"function","name":"decimals","stateMutability":"view","inputs":[],"outputs":[{"type":"uint8"}]},
	 {"type":"function","name":"balanceOf","stateMutability":"view","inputs":[{"type":"address"}],"outputs":[{"type":"uint256"}]},
	 {"type":"function","name":"allowance","stateMutability":"view","inputs":[{"type":"address"},{"type":"address"}],"outputs":[{"type":"uint256"}]},
	 {"type":"function","name":"approve","stateMutability":"nonpayable","inputs":[{"type":"address"},{"type":"uint256"}],"outputs":[{"type":"bool"}]},
	 {"type":"function","name":"deposit","stateMutability":"payable","inputs":[],"outputs":[]}
	]`))
	if err != nil {
		panic(err)
	}
	return a
}()

// ChainID is the ZVM's EVM chain id.
const ChainID = 7340469

// MaxRecipients is the contract's MAX_RECIPIENTS.
const MaxRecipients = 100

// Pack encodes a call.
func (a *Artifact) Pack(method string, args ...any) []byte {
	data, err := a.ABI.Pack(method, args...)
	if err != nil {
		panic(fmt.Errorf("%s.%s: %w", a.ContractName, method, err))
	}
	return data
}

// DeployData is the creation code followed by the encoded constructor
// arguments.
func (a *Artifact) DeployData(args ...any) ([]byte, error) {
	ctor, err := a.ABI.Pack("", args...)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), a.Bytecode...), ctor...), nil
}
