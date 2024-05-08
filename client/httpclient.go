package client

import (
	"net/http"
	"github.com/nsandomeno/btcwallet-client"
)

// WalletClient struct
// client for the JSON-RPC BtcWallet API
type WalletClient struct {
	Config *walletclient.Config
	Client *http.Client
}

// Create new WalletClient instance
func NewWalletClient(config *walletclient.Config) *WalletClient {
	// create the wallet instance
	return &WalletClient{
		Config: config,
	}
}