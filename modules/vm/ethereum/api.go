package ethereum

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/spf13/cast"

	"github.com/cosmos/cosmos-sdk"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/modules/base"
	"github.com/cosmos/cosmos-sdk/stack"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/tendermint/go-wire"
	"github.com/tendermint/go-wire/data"
	rpcclient "github.com/tendermint/tendermint/rpc/client"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	cmn "github.com/tendermint/tmlibs/common"

	"github.com/CyberMiles/travis/modules/auth"
	"github.com/CyberMiles/travis/modules/keys"
	"github.com/CyberMiles/travis/modules/nonce"
	"github.com/CyberMiles/travis/modules/stake"
	"github.com/CyberMiles/travis/modules/governance"
)

// We must implement our own net service since we don't have access to `internal/ethapi`

// NetRPCService mirrors the implementation of `internal/ethapi`
// #unstable
type NetRPCService struct {
	networkVersion uint64
}

// NewNetRPCService creates a new net API instance.
// #unstable
func NewNetRPCService(networkVersion uint64) *NetRPCService {
	return &NetRPCService{networkVersion}
}

// Listening returns an indication if the node is listening for network connections.
// #unstable
func (s *NetRPCService) Listening() bool {
	return true // always listening
}

// PeerCount returns the number of connected peers
// #unstable
func (s *NetRPCService) PeerCount() hexutil.Uint {
	return hexutil.Uint(0)
}

// Version returns the current ethereum protocol version.
// #unstable
func (s *NetRPCService) Version() string {
	return fmt.Sprintf("%d", s.networkVersion)
}

// CmtRPCService offers cmt related RPC methods
type CmtRPCService struct {
	backend *Backend
}

func NewCmtRPCService(b *Backend) *CmtRPCService {
	return &CmtRPCService{
		backend: b,
	}
}

func (s *CmtRPCService) GetBlock(height uint64) (*ctypes.ResultBlock, error) {
	h := cast.ToInt64(height)
	return s.backend.localClient.Block(&h)
}

func (s *CmtRPCService) GetTransaction(hash string) (*ctypes.ResultTx, error) {
	bkey, err := hex.DecodeString(cmn.StripHex(hash))
	if err != nil {
		return nil, err
	}
	return s.backend.localClient.Tx(bkey, false)
}

func (s *CmtRPCService) GetTransactionFromBlock(height uint64, index int64) (*ctypes.ResultTx, error) {
	h := cast.ToInt64(height)
	block, err := s.backend.localClient.Block(&h)
	if err != nil {
		return nil, err
	}
	if index >= block.Block.NumTxs {
		return nil, errors.New(fmt.Sprintf("No transaction in block %d, index %d. ", height, index))
	}
	hash := block.Block.Txs[index].Hash()
	return s.GetTransaction(hex.EncodeToString(hash))
}

// StakeRPCService offers stake related RPC methods
type StakeRPCService struct {
	backend *Backend
	am      *accounts.Manager
}

// NewStakeRPCAPI create a new StakeRPCAPI.
func NewStakeRPCService(b *Backend) *StakeRPCService {
	return &StakeRPCService{
		backend: b,
		am:      b.ethereum.AccountManager(),
	}
}

func (s *StakeRPCService) getChainID() (string, error) {
	if s.backend.chainID == "" {
		return "", errors.New("Empty chain id. Please wait for tendermint to finish starting up. ")
	}

	return s.backend.chainID, nil
}

func (s *StakeRPCService) GetSequence(address string) (*uint32, error) {
	signers := []sdk.Actor{getSignerAct(address)}
	var sequence uint32
	err := s.getSequence(signers, &sequence)
	return &sequence, err
}

type DeclareCandidacyArgs struct {
	Sequence uint32 `json:"sequence"`
	From     string `json:"from"`
	PubKey   string `json:"pubKey"`
}

func (s *StakeRPCService) DeclareCandidacy(args DeclareCandidacyArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	tx, err := s.prepareDeclareCandidacyTx(args)
	if err != nil {
		return nil, err
	}
	return s.broadcastTx(tx)
}

func (s *StakeRPCService) prepareDeclareCandidacyTx(args DeclareCandidacyArgs) (sdk.Tx, error) {
	pubKey, err := stake.GetPubKey(args.PubKey)
	if err != nil {
		return sdk.Tx{}, err
	}
	tx := stake.NewTxDeclareCandidacy(pubKey)
	return s.wrapAndSignTx(tx, args.From, args.Sequence)
}

type WithdrawCandidacyArgs struct {
	Sequence uint32 `json:"sequence"`
	From     string `json:"from"`
}

func (s *StakeRPCService) WithdrawCandidacy(args WithdrawCandidacyArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	tx, err := s.prepareWithdrawCandidacyTx(args)
	if err != nil {
		return nil, err
	}
	return s.broadcastTx(tx)
}

func (s *StakeRPCService) prepareWithdrawCandidacyTx(args WithdrawCandidacyArgs) (sdk.Tx, error) {
	address := common.HexToAddress(args.From)
	tx := stake.NewTxWithdraw(address)
	return s.wrapAndSignTx(tx, args.From, args.Sequence)
}

type EditCandidacyArgs struct {
	Sequence   uint32 `json:"sequence"`
	From       string `json:"from"`
	NewAddress string `json:"newAddress"`
}

func (s *StakeRPCService) EditCandidacy(args EditCandidacyArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	tx, err := s.prepareEditCandidacyTx(args)
	if err != nil {
		return nil, err
	}
	return s.broadcastTx(tx)
}

func (s *StakeRPCService) prepareEditCandidacyTx(args EditCandidacyArgs) (sdk.Tx, error) {
	if len(args.NewAddress) == 0 {
		return sdk.Tx{}, fmt.Errorf("must provide new address")
	}
	address := common.HexToAddress(args.NewAddress)
	tx := stake.NewTxEditCandidacy(address)
	return s.wrapAndSignTx(tx, args.From, args.Sequence)
}

type ProposeSlotArgs struct {
	Sequence    uint32 `json:"sequence"`
	From        string `json:"from"`
	Amount      int64  `json:"amount"`
	ProposedRoi int64  `json:"proposedRoi"`
}

func (s *StakeRPCService) ProposeSlot(args ProposeSlotArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	tx, err := s.prepareProposeSlotTx(args)
	if err != nil {
		return nil, err
	}
	return s.broadcastTx(tx)
}

func (s *StakeRPCService) prepareProposeSlotTx(args ProposeSlotArgs) (sdk.Tx, error) {
	address := common.HexToAddress(args.From)
	tx := stake.NewTxProposeSlot(address, args.Amount, args.ProposedRoi)
	return s.wrapAndSignTx(tx, args.From, args.Sequence)
}

type AcceptSlotArgs struct {
	Sequence uint32 `json:"sequence"`
	From     string `json:"from"`
	Amount   int64  `json:"amount"`
	SlotId   string `json:"slotId"`
}

func (s *StakeRPCService) AcceptSlot(args AcceptSlotArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	tx, err := s.prepareAcceptSlotTx(args)
	if err != nil {
		return nil, err
	}
	return s.broadcastTx(tx)
}

func (s *StakeRPCService) prepareAcceptSlotTx(args AcceptSlotArgs) (sdk.Tx, error) {
	tx := stake.NewTxAcceptSlot(args.Amount, args.SlotId)
	return s.wrapAndSignTx(tx, args.From, args.Sequence)
}

type WithdrawSlotArgs struct {
	Sequence uint32 `json:"sequence"`
	From     string `json:"from"`
	Amount   int64  `json:"amount"`
	SlotId   string `json:"slotId"`
}

func (s *StakeRPCService) WithdrawSlot(args WithdrawSlotArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	tx, err := s.prepareWithdrawSlotTx(args)
	if err != nil {
		return nil, err
	}
	return s.broadcastTx(tx)
}

func (s *StakeRPCService) prepareWithdrawSlotTx(args WithdrawSlotArgs) (sdk.Tx, error) {
	tx := stake.NewTxWithdrawSlot(args.Amount, args.SlotId)
	return s.wrapAndSignTx(tx, args.From, args.Sequence)
}

type CancelSlotArgs struct {
	Sequence uint32 `json:"sequence"`
	From     string `json:"from"`
	SlotId   string `json:"slotId"`
}

func (s *StakeRPCService) CancelSlot(args CancelSlotArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	tx, err := s.prepareCancelSlotTx(args)
	if err != nil {
		return nil, err
	}
	return s.broadcastTx(tx)
}

func (s *StakeRPCService) prepareCancelSlotTx(args CancelSlotArgs) (sdk.Tx, error) {
	address := common.HexToAddress(args.From)
	tx := stake.NewTxCancelSlot(address, args.SlotId)
	return s.wrapAndSignTx(tx, args.From, args.Sequence)
}

func (s *StakeRPCService) wrapAndSignTx(tx sdk.Tx, address string, sequence uint32) (sdk.Tx, error) {
	// wrap
	// only add the actual signer to the nonce
	signers := []sdk.Actor{getSignerAct(address)}
	if sequence <= 0 {
		// calculate default sequence
		err := s.getSequence(signers, &sequence)
		if err != nil {
			return sdk.Tx{}, err
		}
		sequence = sequence + 1
	}
	tx = nonce.NewTx(sequence, signers, tx)

	chainID, err := s.getChainID()
	if err != nil {
		return sdk.Tx{}, err
	}
	tx = base.NewChainTx(chainID, 0, tx)
	tx = auth.NewSig(tx).Wrap()

	// sign
	err = s.signTx(tx, address)
	if err != nil {
		return sdk.Tx{}, err
	}
	return tx, err
}

func (s *StakeRPCService) getSequence(signers []sdk.Actor, sequence *uint32) error {
	key := stack.PrefixedKey(nonce.NameNonce, nonce.GetSeqKey(signers))
	result, err := s.backend.localClient.ABCIQuery("/key", key)
	if err != nil {
		return err
	}

	if len(result.Response.Value) == 0 {
		return nil
	}
	return wire.ReadBinaryBytes(result.Response.Value, sequence)
}

// sign the transaction with private key
func (s *StakeRPCService) signTx(tx sdk.Tx, address string) error {
	// validate tx client-side
	err := tx.ValidateBasic()
	if err != nil {
		return err
	}

	if sign, ok := tx.Unwrap().(keys.Signable); ok {
		if address == "" {
			return errors.New("address is required to sign tx")
		}
		err := s.sign(sign, address)
		if err != nil {
			return err
		}
	}
	return err
}

func (s *StakeRPCService) sign(data keys.Signable, address string) error {
	ethTx := types.NewTransaction(
		0,
		common.Address([20]byte{}),
		big.NewInt(0),
		big.NewInt(0),
		big.NewInt(0),
		data.SignBytes(),
	)

	addr := common.HexToAddress(address)
	account := accounts.Account{Address: addr}
	wallet, err := s.am.Find(account)
	if err != nil {
		return err
	}
	signed, err := wallet.SignTx(account, ethTx, big.NewInt(15)) //TODO: use defaultEthChainId
	if err != nil {
		return err
	}

	return data.Sign(signed)
}

func (s *StakeRPCService) broadcastTx(tx sdk.Tx) (*ctypes.ResultBroadcastTxCommit, error) {
	key := wire.BinaryBytes(tx)
	return s.backend.localClient.BroadcastTxCommit(key)
}

func getSignerAct(address string) (res sdk.Actor) {
	// this could be much cooler with multisig...
	signer := common.HexToAddress(address)
	res = auth.SigPerm(signer.Bytes())
	return res
}

type StakeQueryResult struct {
	Height int64       `json:"height"`
	Data   interface{} `json:"data"`
}

func (s *StakeRPCService) QueryValidators(height uint64) (*StakeQueryResult, error) {
	var candidates stake.Candidates
	//key := stack.PrefixedKey(stake.Name(), stake.CandidatesPubKeysKey)
	h, err := s.getParsed("/validators", []byte{0}, &candidates, height)
	if err != nil {
		return nil, err
	}

	return &StakeQueryResult{h, candidates}, nil
}

func (s *StakeRPCService) QueryValidator(address string, height uint64) (*StakeQueryResult, error) {
	var candidate stake.Candidate
	h, err := s.getParsed("/validator", []byte(address), &candidate, height)
	if err != nil {
		return nil, err
	}

	return &StakeQueryResult{h, candidate}, nil
}

func (s *StakeRPCService) QuerySlots(height uint64) (*StakeQueryResult, error) {
	var slots []*stake.Slot
	h, err := s.getParsed("/slots", []byte{0}, &slots, height)
	if err != nil {
		return nil, err
	}

	return &StakeQueryResult{h, slots}, nil
}

func (s *StakeRPCService) QuerySlot(slotId string, height uint64) (*StakeQueryResult, error) {
	var slot stake.Slot
	h, err := s.getParsed("/slot", []byte(slotId), &slot, height)
	if err != nil {
		return nil, err
	}

	return &StakeQueryResult{h, slot}, nil
}

func (s *StakeRPCService) QueryDelegator(address string, height uint64) (*StakeQueryResult, error) {
	var slotDelegates []*stake.SlotDelegate
	h, err := s.getParsed("/delegator", []byte(address), &slotDelegates, height)
	if err != nil {
		return nil, err
	}

	return &StakeQueryResult{h, slotDelegates}, nil
}

func (s *StakeRPCService) getParsed(path string, key []byte, data interface{}, height uint64) (int64, error) {
	bs, h, err := s.get(path, key, cast.ToInt64(height))
	if err != nil {
		return 0, err
	}
	if len(bs) == 0 {
		return h, client.ErrNoData()
	}
	err = wire.ReadBinaryBytes(bs, data)
	if err != nil {
		return 0, err
	}
	return h, nil
}

func (s *StakeRPCService) get(path string, key []byte, height int64) (data.Bytes, int64, error) {
	node := s.backend.localClient
	resp, err := node.ABCIQueryWithOptions(path, key,
		rpcclient.ABCIQueryOptions{Trusted: true, Height: int64(height)})
	if resp == nil {
		return nil, height, err
	}
	return data.Bytes(resp.Response.Value), resp.Response.Height, err
}

// GovernanceRPCService offers governance related RPC methods
type GovernanceRPCService struct {
	backend *Backend
	am      *accounts.Manager
}

// NewGovernanceRPCAPI create a new StakeRPCAPI.
func NewGovernanceRPCService(b *Backend) *GovernanceRPCService {
	return &GovernanceRPCService{
		backend: b,
		am:      b.ethereum.AccountManager(),
	}
}

type GovernanceProposalArgs struct {
	Proposer string `json:"proposer"`
	From     string `json:"from"`
	To       string `json:"to"`
	Amount   string `json:"amount"`
	Reason   string `json:"reason"`
}

func (s *GovernanceRPCService) Propose(args GovernanceProposalArgs) (*ctypes.ResultBroadcastTxCommit, error) {
	proposer := common.HexToAddress(args.Proposer)
	fromAddr := common.HexToAddress(args.From)
	toAddr   := common.HexToAddress(args.To)
	amount   := new(big.Int)
	amount.SetString(args.Amount, 10)

	tx := governance.NewTxPropose(proposer, fromAddr, toAddr, amount, args.Reason)
	s.wrapAndSignTx(tx, args.Proposer)

	return s.broadcastTx(tx)
}

func (s *GovernanceRPCService) wrapAndSignTx(tx sdk.Tx, address string) (sdk.Tx, error) {
	// wrap
	// only add the actual signer to the nonce
	signers := []sdk.Actor{getSignerAct(address)}
	var sequence  uint32
	// calculate default sequence
	err := s.getSequence(signers, &sequence)
	if err != nil {
		return sdk.Tx{}, err
	}
	sequence = sequence + 1
	tx = nonce.NewTx(sequence, signers, tx)

	chainID, err := s.getChainID()
	if err != nil {
		return sdk.Tx{}, err
	}
	tx = base.NewChainTx(chainID, 0, tx)
	tx = auth.NewSig(tx).Wrap()

	// sign
	err = s.signTx(tx, address)
	if err != nil {
		return sdk.Tx{}, err
	}
	return tx, err
}

// sign the transaction with private key
func (s *GovernanceRPCService) signTx(tx sdk.Tx, address string) error {
	// validate tx client-side
	err := tx.ValidateBasic()
	if err != nil {
		return err
	}

	if sign, ok := tx.Unwrap().(keys.Signable); ok {
		if address == "" {
			return errors.New("address is required to sign tx")
		}
		err := s.sign(sign, address)
		if err != nil {
			return err
		}
	}
	return err
}

func (s *GovernanceRPCService) sign(data keys.Signable, address string) error {
	ethTx := types.NewTransaction(
		0,
		common.Address([20]byte{}),
		big.NewInt(0),
		big.NewInt(0),
		big.NewInt(0),
		data.SignBytes(),
	)

	addr := common.HexToAddress(address)
	account := accounts.Account{Address: addr}
	wallet, err := s.am.Find(account)
	if err != nil {
		return err
	}
	signed, err := wallet.SignTx(account, ethTx, big.NewInt(15)) //TODO: use defaultEthChainId
	if err != nil {
		return err
	}

	return data.Sign(signed)
}

func (s *GovernanceRPCService) broadcastTx(tx sdk.Tx) (*ctypes.ResultBroadcastTxCommit, error) {
	key := wire.BinaryBytes(tx)
	return s.backend.localClient.BroadcastTxCommit(key)
}

func (s *GovernanceRPCService) getSequence(signers []sdk.Actor, sequence *uint32) error {
	key := stack.PrefixedKey(nonce.NameNonce, nonce.GetSeqKey(signers))
	result, err := s.backend.localClient.ABCIQuery("/key", key)
	if err != nil {
		return err
	}

	if len(result.Response.Value) == 0 {
		return nil
	}
	return wire.ReadBinaryBytes(result.Response.Value, sequence)
}

func (s *GovernanceRPCService) getChainID() (string, error) {
	if s.backend.chainID == "" {
		return "", errors.New("Empty chain id. Please wait for tendermint to finish starting up. ")
	}

	return s.backend.chainID, nil
}
