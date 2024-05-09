package walletclient

import (
	"path/filepath"
	"github.com/btcsuite/btcd/btcutil"
)

const (
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
	WsEndpoint       string
	// params refers to the bitcoin network i.e. testnet, mainnet, etc.
	Params           string
	DisableTLS       bool
	// bytes for pem encoded cert chain used for the tls connection. No effect if the DisableTLS param is true.
	Certificates     []byte
	// DisableAutoReconnect specifies the client should not automatically reconnect to the server if the connection is lost.
	DisableAutoReconnect bool
	// DisableConnectOnNew specifies the client should not automatically connect to the server when a new client is created.
	DisableConnectOnNew bool
	// HTTPPostMode instructs the client to run using multiple independent connections issuing HTTP POST requests instead of using the default
	// of websockets.
	HTTPPostMode bool 
}

func NewConfig(dir string, serverRpcCert string, serverUri string) *Config {
	// load the rpc certification
	walletCertDir := btcutil.AppDataDir(dir, false)
	// tls cert full path
	certFileName := filepath.Join(walletCertDir, serverRpcCert)
	// TODO read and add certificates here: see the httpclient for reference

	// 
	// load passphrase bytes
	passphrase := []byte("")
	// return config instance
	return &Config{
		Directory:        walletCertDir,
		ServerRpcCert:    certFileName,
		ServerUri:        serverUri,
		PublicPassphrase: passphrase,
		RpcUser:         RPCUSER,
		RpcPass:         RPCPASS,
		WsEndpoint:      "ws",
		Params:          "testnet",
		DisableTLS:      false,
		Certificates:    nil,
		DisableAutoReconnect: false,
		DisableConnectOnNew: false,
		HTTPPostMode: false,
	}
}
// return values that are required for JSON-RPC Authentication
func (config *Config) GetAuth() (username string, passphrase string, err error) {
	// TODO cookie auth not yet implemented, these are required
	return config.RpcUser, config.RpcPass, nil
}