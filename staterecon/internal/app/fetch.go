package app

import (
	"context"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/rpc"
)

func fetchChainBasics(
	ctx context.Context,
	client *rpc.Client,
	accounts []models.AccountSnapshot,
	blockTag string,
) []chainBasicState {
	balances := make([]string, len(accounts))
	nonces := make([]string, len(accounts))
	elems := make([]rpc.BatchElem, 0, len(accounts)*2)

	for index := range accounts {
		address := accounts[index].Address
		elems = append(
			elems,
			rpc.BatchElem{
				Method: "eth_getBalance",
				Params: []any{address, blockTag},
				Result: &balances[index],
			},
			rpc.BatchElem{
				Method: "eth_getTransactionCount",
				Params: []any{address, blockTag},
				Result: &nonces[index],
			},
		)
	}

	_ = client.BatchCall(ctx, elems)

	out := make([]chainBasicState, len(accounts))
	for index := range accounts {
		out[index] = chainBasicState{
			Errors: make(map[string]string, 2),
		}

		balanceElem := elems[index*2]
		if balanceElem.Err != nil {
			out[index].Errors["balance"] = balanceElem.Err.Error()
		} else {
			normalized, err := models.NormalizeQuantity(balances[index])
			if err != nil {
				out[index].Errors["balance"] = err.Error()
			} else {
				out[index].Balance = normalized
			}
		}

		nonceElem := elems[index*2+1]
		if nonceElem.Err != nil {
			out[index].Errors["nonce"] = nonceElem.Err.Error()
		} else {
			normalized, err := models.NormalizeQuantity(nonces[index])
			if err != nil {
				out[index].Errors["nonce"] = err.Error()
			} else {
				out[index].Nonce = normalized
			}
		}
	}

	return out
}
