package client

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/btcutil"

	//"github.com/labstack/echo/v4"
	// "golang.org/x/net/context"
	// "google.golang.org/grpc"
	// "google.golang.org/grpc/credentials"
)

// WalletClient struct
// client for the JSON-RPC BtcWallet API
type WalletClient struct {
	Config *Config
	Client * http.Client
}

// Create new WalletClient instance
func NewWalletClient(config *Config) *WalletClient {
	// create the wallet instance
	return &WalletClient{
		Config: config,
	}
}