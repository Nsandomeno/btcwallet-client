package main

/// * THANKS TO THE AUTHORS OF BTCSUITE - SPECIFIC INSPIRATION FOR THIS CODE FROM BTCCTL:
/// * https://github.com/btcsuite/btcd/blob/master/cmd/btcctl/httpclient.go
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



func main() {
	// load config
	config := NewConfig(DIR, SERVER_RPC_CERT, SERVER_URI)
	// start rpc connection to wallet server
	wallet := NewWalletClient(&config)
	// open wallet

	// e := echo.New()
	// e.GET("/", func(c echo.Context) error {
	// 	return c.String(http.StatusOK, "Hello, World!")
	// })
	// e.Logger.Fatal(e.Start(":8080"))
}


/// the Config struct for the Btcwallet Client instance
type Config struct {
	OsBasePath       string
	Directory        string
	ServerRpcCert    string
	ServerUri        string
	PublicPassphrase []byte
	RpcUser		     string
	RpcPass		     string
}






func (w *WalletClient) NewHttpClient() (*http.Client, error) {
	// create a dial function (TODO is this really needed if not using a proxy?)
	var dial func(network, addr string) (net.Conn, error)
	// configure tls
	var tlsConfig *tls.Config
	pem, err := os.ReadFile(w.Config.ServerRpcCert)
	if err != nil {
		// TODO handle error
		fmt.Println("failed to read wallet server-side TLS certificate")
		return nil, errors.New("failed to read wallet server-side TLS certificate")
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	// populate the tlsconfig instance
	tlsConfig = &tls.Config{
		RootCAs: pool,
		InsecureSkipVerify: false,
	}
	// create http client from config after TLS
	httpClient := &http.Client{
		Transport: &http.Transport{
			Dial: dial,
			TLSClientConfig: tlsConfig,
		},
	}
	return httpClient, nil
}

// SendPostRequest is modeled after BTCCTL's `sendPostRequest` function and general
// consumption of BtcWallet's JSON-RPC API: https://github.com/btcsuite/btcd/tree/master/cmd/btcctl

// Sends JSON-RPC POST request to the wallet server and unmarshals the response, returning either
// the result field or the error field
func (w *WalletClient) SendPostRequest(marshalledJSON []byte) ([]byte, error) {
	// generate request to Wallet server
	protocol := "https"
	url := protocol + "://" + w.Config.ServerUri
	bodyReader := bytes.NewReader(marshalledJSON)
	httpRequest, err := http.NewRequest("POST", url, bodyReader)
	if err != nil {
		// TODO handle error
		fmt.Println("Failed to create new request")
		return nil, errors.New("failed to create new request")
	}
	// set close (TODO what is this?)
	httpRequest.Close = true
	// set headers
	httpRequest.Header.Set("Content-Type", "application/json")
	// auth header (TODO abstract to own method)
	httpRequest.SetBasicAuth(w.Config.RpcUser, w.Config.RpcPass)
	// create the http client
	httpClient, err := w.NewHttpClient()
	if err != nil {
		// TODO handle error
		fmt.Println("Failed to create new http client")
		return nil, errors.New("failed to create new http client")
	}
	// send the request
	httpResponse, err := httpClient.Do(httpRequest)
	if err != nil {
		// TODO handle error
		fmt.Println("Failed to send request")
		return nil, errors.New("failed to send request")
	}
	// finalize request reading and handling
	respBytes, err := io.ReadAll(httpResponse.Body)
	httpResponse.Body.Close()
	if err != nil {
		// TODO handle error
		fmt.Println("Failed to read response")
		return nil, errors.New("failed to read response")
	}
	// check for error codes
	// * DIRECT INSPIRATION FOR HANDLING CODES: https://github.com/btcsuite/btcd/blob/6b197d38d745048c5fe2a895010c9c0a4d9ab2a6/cmd/btcctl/httpclient.go#L107
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		// * DIRECT INSPIRATION FOR HANDLING EMPTY SERVER BODY: https://github.com/btcsuite/btcd/blob/6b197d38d745048c5fe2a895010c9c0a4d9ab2a6/cmd/btcctl/httpclient.go#L112
		if len(respBytes) == 0 {
			return nil, fmt.Errorf("%d %s", httpResponse.StatusCode, http.StatusText(httpResponse.StatusCode))
		}
		return nil, errors.New(string(respBytes))
	}
	// decode the response
	var resp btcjson.Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		// TODO handle error
		fmt.Println("Failed to unmarshal response")
		return nil, errors.New("failed to unmarshal response")
	}
	// check for error
	if resp.Error != nil {
		// TODO handle error

		fmt.Println("Failed to handle error")
		return nil, errors.New("failed to handle error")
	}
	// return the result
	return resp.Result, nil
}
