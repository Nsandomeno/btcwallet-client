package client

import (
	"github.com/nsandomeno/btcwallet-client"
)

// WalletClient struct
// client for the JSON-RPC BtcWallet API
type RemoteWallet struct {
	Config *walletclient.Config
	// the BtcWallet RPC Client
	RpcClient *Client
}


// Create new WalletClient instance
func NewRemoteWallet(config *walletclient.Config) *RemoteWallet {
	// create the wallet instance

	// TODO initialize the wallet client using the passed Config
	return &RemoteWallet{
		Config: config,
	}
}
