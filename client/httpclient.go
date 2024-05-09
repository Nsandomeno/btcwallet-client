package client
// * THANKS TO THE AUTHORS OF BTCSUITE - SPECIFIC INSPIRATION FOR THIS FILE FROM btcd/rpcclient
// * ISC LICENSE AT ROOT OF THIS REPOSTIORY
import (
	"bytes"
	"container/list"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/websocket"
	wallet "github.com/nsandomeno/btcwallet-client"
)
var (
	// ErrInvalidAuth is an error to describe the condition where the client
	// is either unable to authenticate or the specified endpoint is
	// incorrect.
	ErrInvalidAuth = errors.New("authentication failure")

	// ErrInvalidEndpoint is an error to describe the condition where the
	// websocket handshake failed with the specified endpoint.
	ErrInvalidEndpoint = errors.New("the endpoint either does not support " +
		"websockets or does not exist")

	// ErrClientNotConnected is an error to describe the condition where a
	// websocket client has been created, but the connection was never
	// established.  This condition differs from ErrClientDisconnect, which
	// represents an established connection that was lost.
	ErrClientNotConnected = errors.New("the client was never connected")

	// ErrClientDisconnect is an error to describe the condition where the
	// client has been disconnected from the RPC server.  When the
	// DisableAutoReconnect option is not set, any outstanding futures
	// when a client disconnect occurs will return this error as will
	// any new requests.
	ErrClientDisconnect = errors.New("the client has been disconnected")

	// ErrClientShutdown is an error to describe the condition where the
	// client is either already shutdown, or in the process of shutting
	// down.  Any outstanding futures when a client shutdown occurs will
	// return this error as will any new requests.
	ErrClientShutdown = errors.New("the client has been shutdown")

	// ErrNotWebsocketClient is an error to describe the condition of
	// calling a Client method intended for a websocket client when the
	// client has been configured to run in HTTP POST mode instead.
	ErrNotWebsocketClient = errors.New("client is not configured for " +
		"websockets")

	// ErrClientAlreadyConnected is an error to describe the condition where
	// a new client connection cannot be established due to a websocket
	// client having already connected to the RPC server.
	ErrClientAlreadyConnected = errors.New("websocket client has already " +
		"connected")
)

const (
	// sendBufferSize is the number of elements the websocket send channel
	// can queue before blocking.
	sendBufferSize = 50

	// sendPostBufferSize is the number of elements the HTTP POST send
	// channel can queue before blocking.
	sendPostBufferSize = 100

	// connectionRetryInterval is the amount of time to wait in between
	// retries when automatically reconnecting to an RPC server.
	connectionRetryInterval = time.Second * 5

	// requestRetryInterval is the initial amount of time to wait in between
	// retries when sending HTTP POST requests.
	requestRetryInterval = time.Millisecond * 500
)
// FutureGetBulkResult waits for the responses promised by the future
// and returns them in a channel
type FutureGetBulkResult chan *Response

// newFutureError returns a new future result channel that already has the
// passed error waitin on the channel with the reply set to nil.  This is useful
// to easily return errors from the various Async functions.
func newFutureError(err error) chan *Response {
	responseChan := make(chan *Response, 1)
	responseChan <- &Response{err: err}
	return responseChan
}

// Expose newFutureError for developer usage when creating custom commands.
func NewFutureError(err error) chan *Response {
	return newFutureError(err)
}

// ReceiveFuture receives from the passed futureResult channel to extract a
// reply or any errors.  The examined errors include an error in the
// futureResult and the error in the reply from the server.  This will block
// until the result is available on the passed channel.
func ReceiveFuture(f chan *Response) ([]byte, error) {
	// Wait for a response on the returned channel.
	r := <-f
	return r.result, r.err
}

// Receive waits for the response promised by the future and returns an map
// of results by request id
func (r FutureGetBulkResult) Receive() (BulkResult, error) {
	m := make(BulkResult)
	res, err := ReceiveFuture(r)
	if err != nil {
		return nil, err
	}
	var arr []IndividualBulkResult
	err = json.Unmarshal(res, &arr)
	if err != nil {
		return nil, err
	}

	for _, results := range arr {
		m[results.Id] = results
	}

	return m, nil
}

// ignoreResends is a set of all methods for requests that are "long running"
// are not be reissued by the client on reconnect.
var ignoreResends = map[string]struct{}{
	"rescan": {},
}

// IndividualBulkResult represents one result
// from a bulk json rpc api
type IndividualBulkResult struct {
	Result interface{}       `json:"result"`
	Error  *btcjson.RPCError `json:"error"`
	Id     uint64            `json:"id"`
}

type BulkResult = map[uint64]IndividualBulkResult


//inMessage top of funnel structure for incoming messages
type inMessage struct {
	ID *float64 `json:"id"`
	*rawNotification
	*rawResponse
}


// rawNotification is a partially-unmarshaled JSON-RPC notification.
type rawNotification struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}


// rawResponse is a partially-unmarshaled JSON-RPC response.  For this
// to be valid (according to JSON-RPC 1.0 spec), ID may not be nil.
type rawResponse struct {
	Result json.RawMessage   `json:"result"`
	Error  *btcjson.RPCError `json:"error"`
}


// checks a rawResponse for populated error attribute first, returns an error if populated or else
// the raw bytes of the result
func (r rawResponse) result() (result []byte, err error) {
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}


// Response is the raw bytes of a JSON-RPC result, or the error if the response
// error object was non-null.
type Response struct {
	result []byte
	err    error
}


type jsonRequest struct {
	id uint64
	method string
	cmd interface{}
	marshalledJSON []byte
	responseChan chan *Response
}


func newHTTPClient(config *wallet.Config) (*http.Client, error) {
	// TODO move this to Config New method

	//pem, err := os.ReadFile(config.ServerRpcCert)
	// if err != nil {
	// 	// TODO handle error
	// 	fmt.Println("failed to read wallet server-side TLS certificate")
	// 	return nil, errors.New("failed to read wallet server-side TLS certificate")
	// }

	// configure tls
	// TODO implement disabled TLS
	var tlsConfig *tls.Config
	if len(config.Certificates) > 0 {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(config.Certificates)
		// populate the tlsconfig instance
		tlsConfig = &tls.Config{
			RootCAs: pool,
		}
	}
	// create http client from config after TLS
	httpClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	return &httpClient, nil
}

// opens websocket connection to wallet server based on config
func dial(config *wallet.Config) (*websocket.Conn, error) {
	// configure tls
	var tlsConfig *tls.Config
	var scheme = "wss"
	// TODO implement disabled TLS
	if len(config.Certificates) > 0 {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(config.Certificates)
		tlsConfig.RootCAs = pool
	}
	// create websocket dialer
	dialer := websocket.Dialer{TLSClientConfig: tlsConfig}
	// rpc auth
	user, pass, err := config.GetAuth()
	if err != nil {
		// TODO handle error
		return nil, err
	}
	login := user + ":" + pass
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(login))
	requestHeader := make(http.Header)
	requestHeader.Add("Authorization", auth)
	// TODO add in ExtraHeaders

	// dial
	url := fmt.Sprintf("%s://%s/%s", scheme, config.ServerUri, config.WsEndpoint)
	wsConn, resp, err := dialer.Dial(url, requestHeader)
	if err != nil {
		// TODO handle error

		// check error details
		if err != websocket.ErrBadHandshake || resp == nil {
			return nil, err
		}
		// check for error codes
		if resp.StatusCode == http.StatusUnauthorized ||
			resp.StatusCode == http.StatusForbidden {
			return nil, errors.New("authentication failed")
		}
		// (a) connection was authenticated 
		// (b) websocket handshake still failed...
		// something is wrong with the server websocket endpoint.
		if resp.StatusCode == http.StatusOK {
			// TODO handle errors
			return nil, errors.New("websocket handshake failed")
		}
		// unknown error
		return nil, errors.New(resp.Status)
	}
	return wsConn, nil
}

// // SendPostRequest is modeled after BTCCTL's `sendPostRequest` function and general
// // consumption of BtcWallet's JSON-RPC API: https://github.com/btcsuite/btcd/tree/master/cmd/btcctl

// // Sends JSON-RPC POST request to the wallet server and unmarshals the response, returning either
// // the result field or the error field
func sendPostRequest(config *wallet.Config, marshalledJSON []byte) ([]byte, error) {
	// generate request to Wallet server
	protocol := "https"
	url := protocol + "://" + config.ServerUri
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
	httpRequest.SetBasicAuth(config.RpcUser, config.RpcPass)
	// create the http client
	httpClient, err := newHTTPClient(config)
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

	// TODO this may by specific to btcd, need to check
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

// Client represents the BtcWallet RPC client which will also have to comply with the lnd.lndwallet.WalletController
// interface here: https://github.com/lightningnetwork/lnd/blob/4a9ab6e538e4c69a6cd5e91f1ce1752d9c360c90/lnwallet/interface.go#L228

// handles the conversion of request/response types to the underlying JSON types expected by the wallet server.
type Client struct {
	id      uint64 // atomic, must stay 64-bit aligned
	config *wallet.Config
	// chainParams hold metadata related to bitcoin network in use
	chainParams *chaincfg.Params
	wsConn      *websocket.Conn
	httpClient  *http.Client
	// TODO should we apply backendVersion and backendVersionMu to the connected node? Or, remove?
	nodeVersionMu sync.Mutex
	nodeVersion   NodeVersion
	// mtx is used to protect access to connection related fields
	mtx sync.Mutex
	// whether or not server is disconnected
	disconnected bool
	// whether or not to batch requests, false unless changed by Batch()
	batch        bool
	batchList    *list.List
	// retryCount holds the number of times the client has tried to 
	// reconnect to the RPC server
	retryCount int64
	// track the command and their response to channel by ID
	requestLock sync.Mutex
	requestMap  map[uint64]*list.Element
	requestList *list.List
	// Notifications.
	ntfnHandlers  *NotificationHandlers
	ntfnStateLock sync.Mutex
	ntfnState     *notificationState
	// Networking infrastructure.
	sendChan        chan []byte
	sendPostChan    chan *jsonRequest
	connEstablished chan struct{}
	disconnect      chan struct{}
	shutdown        chan struct{}
	wg              sync.WaitGroup
}

// New creates RPC client based on the config
// nofification handlers param can be nil if you don't want to handle notifications
// 