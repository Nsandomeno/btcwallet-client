package walletclient

import (
	"path/filepath"
	"github.com/btcsuite/btcd/btcutil"
)

var (
	BASE_DIR        = "~/Library/Application Support/"
	DIR             = "btcwalletclient"
	SERVER_RPC_CERT = "rpc.cert"
	RPCUSER		    = "na-dev"
	RPCPASS		    = "password123"
	SERVER_URI      = "127.0.0.1:5000"
	FALLBACK_HOST   = "127.0.0.1"
)

// config struct
type Config struct {
	Directory        string
	ServerRpcCert    string
	ServerUri        string
	PublicPassphrase []byte
	RpcUser		     string
	RpcPass		     string
}

func NewConfig(dir string, serverRpcCert string, serverUri string) Config {
	// load the rpc certification
	walletCertDir := btcutil.AppDataDir(dir, false)
	// tls cert full path
	certFileName := filepath.Join(walletCertDir, serverRpcCert)
	// load passphrase bytes
	passphrase := []byte("")
	// return config instance
	return Config{
		Directory:  walletCertDir,
		ServerRpcCert: certFileName,
		ServerUri: serverUri,
		PublicPassphrase: passphrase,
		RpcUser: RPCUSER,
		RpcPass: RPCPASS,
	}
}