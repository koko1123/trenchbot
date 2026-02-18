package executor

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/cindocode/trenchbot/internal/state"
	bnbclient "github.com/cindocode/trenchbot/pkg/bnb"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// bnbEstimatedGasCost is the estimated gas per transaction on BSC (~21000 gas * 3 gwei).
const bnbEstimatedGasCost = 0.0003 // BNB

const fourMemeABI = `[{"inputs":[{"name":"origin","type":"address"},{"name":"token","type":"address"},{"name":"funds","type":"uint256"},{"name":"minAmount","type":"uint256"}],"name":"buyTokenAMAP","outputs":[],"stateMutability":"payable","type":"function"}]`

type FourMemeExecutor struct {
	client        *bnbclient.Client
	proxyContract common.Address
	contractABI   abi.ABI
	log           *slog.Logger
}

func NewFourMemeExecutor(bnbClient *bnbclient.Client, proxyAddr string, log *slog.Logger) (*FourMemeExecutor, error) {
	parsed, err := abi.JSON(strings.NewReader(fourMemeABI))
	if err != nil {
		return nil, fmt.Errorf("parsing four.meme ABI: %w", err)
	}
	return &FourMemeExecutor{
		client:        bnbClient,
		proxyContract: common.HexToAddress(proxyAddr),
		contractABI:   parsed,
		log:           log,
	}, nil
}

func (e *FourMemeExecutor) Chain() state.Chain {
	return state.ChainBNB
}

func (e *FourMemeExecutor) Buy(ctx context.Context, params BuyParams) BuyResult {
	if params.Shadow {
		e.log.Info("SHADOW BUY",
			"chain", "bnb",
			"token", params.TokenAddress,
			"symbol", params.TokenSymbol,
			"amount_bnb", params.Amount,
		)
		return BuyResult{
			Success: true,
			TxHash:  "shadow-" + SafePrefix(params.TokenAddress, 8),
			Price:   1.0,
			Amount:  params.Amount,
			GasCost: bnbEstimatedGasCost,
		}
	}

	// Block live buys until sell is implemented to prevent stranded positions.
	return BuyResult{Error: fmt.Errorf("four.meme live buy blocked: sell not yet implemented")}

	tokenAddr := common.HexToAddress(params.TokenAddress)
	amountWei := etherToWei(params.Amount)
	minAmount := big.NewInt(0) // accept any amount (slippage handled by contract)

	origin := e.client.Address()
	data, err := e.contractABI.Pack("buyTokenAMAP", origin, tokenAddr, amountWei, minAmount)
	if err != nil {
		return BuyResult{Error: fmt.Errorf("packing call data: %w", err)}
	}

	nonce, err := e.client.Eth().PendingNonceAt(ctx, e.client.Address())
	if err != nil {
		return BuyResult{Error: fmt.Errorf("getting nonce: %w", err)}
	}

	gasPrice, err := e.client.Eth().SuggestGasPrice(ctx)
	if err != nil {
		return BuyResult{Error: fmt.Errorf("getting gas price: %w", err)}
	}

	gasLimit, err := e.client.Eth().EstimateGas(ctx, ethereum.CallMsg{
		From:  e.client.Address(),
		To:    &e.proxyContract,
		Value: amountWei,
		Data:  data,
	})
	if err != nil {
		return BuyResult{Error: fmt.Errorf("estimating gas: %w", err)}
	}

	tx := types.NewTransaction(nonce, e.proxyContract, amountWei, gasLimit, gasPrice, data)
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(e.client.ChainID()), e.client.PrivateKey())
	if err != nil {
		return BuyResult{Error: fmt.Errorf("signing tx: %w", err)}
	}

	if err := e.client.Eth().SendTransaction(ctx, signedTx); err != nil {
		return BuyResult{Error: fmt.Errorf("sending tx: %w", err)}
	}

	txHash := signedTx.Hash().Hex()
	e.log.Info("BUY executed",
		"chain", "bnb",
		"token", params.TokenAddress,
		"symbol", params.TokenSymbol,
		"tx", txHash,
		"amount_bnb", params.Amount,
	)

	// Estimate gas cost: gasLimit * gasPrice in BNB.
	gasCostWei := new(big.Int).Mul(new(big.Int).SetUint64(gasLimit), gasPrice)
	gasCostBNB, _ := new(big.Float).Quo(new(big.Float).SetInt(gasCostWei), big.NewFloat(1e18)).Float64()

	return BuyResult{
		Success: true,
		TxHash:  txHash,
		Price:   1.0,
		Amount:  params.Amount,
		GasCost: gasCostBNB,
	}
}

func (e *FourMemeExecutor) Sell(ctx context.Context, params SellParams) SellResult {
	if params.Shadow {
		e.log.Info("SHADOW SELL",
			"chain", "bnb",
			"token", params.TokenAddress,
			"symbol", params.TokenSymbol,
			"pct", params.AmountPct,
		)
		return SellResult{
			Success: true,
			TxHash:  "shadow-sell-" + SafePrefix(params.TokenAddress, 8),
			GasCost: bnbEstimatedGasCost,
		}
	}

	// Four.meme sell requires token approval + swap via PancakeSwap router
	// once the token graduates. This is a placeholder for the sell path.
	e.log.Warn("four.meme live sell not yet implemented",
		"token", params.TokenAddress,
	)
	return SellResult{
		Error: fmt.Errorf("four.meme sell not yet implemented"),
	}
}

func etherToWei(eth float64) *big.Int {
	// Convert BNB (18 decimals) to wei
	weiFloat := new(big.Float).Mul(big.NewFloat(eth), big.NewFloat(1e18))
	wei, _ := weiFloat.Int(nil)
	return wei
}

// Ensure crypto import is used
var _ = crypto.Keccak256
