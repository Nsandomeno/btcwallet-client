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
	"math"
	"net"
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
// or, running in HTTP POST mode (only?)
func New(config *wallet.Config, ntfnHandlers *NotificationHandlers) (*Client, error) {
	// either open websocket connection or create an HTTP client depending on the HTTP Post mode.
	// also, set notification handlers to nil if running in HTTP POST mode
	var wsConn      *websocket.Conn
	var httpClient  *http.Client
	connEstablished := make(chan struct{})
	var start       bool
	// check if HTTP Post mode is enabled
	if config.HTTPPostMode {
		ntfnHandlers = nil 
		// start flag setter
		start = true
		var err error
		// create http client
		httpClient, err = newHTTPClient(config)
		if err != nil {
			// TODO handle error
			return nil, err
		}
	} else {
		if !config.DisableConnectOnNew {
			var err error
			// create websocket connection
			wsConn, err = dial(config)
			if err != nil {
				// TODO handle error
				return nil, err
			}
			// start flag setter
			start = true
		}
	}
	// create new client
	client := &Client{
		config:         config,
		wsConn: 	    wsConn,
		httpClient:     httpClient,
		requestMap:     make(map[uint64]*list.Element),
		requestList:    list.New(),
		batch: 		    false,
		batchList: 	    list.New(),
		ntfnHandlers:   ntfnHandlers,
		ntfnState:      newNotificationState(),
		sendChan:       make(chan []byte, sendBufferSize),
		sendPostChan:   make(chan *jsonRequest, sendPostBufferSize),
		connEstablished: connEstablished,
		disconnect:     make(chan struct{}),
		shutdown:       make(chan struct{}),
	}
	// default network is mainnet
	switch config.Params {
		case "":
			fallthrough
		case chaincfg.MainNetParams.Name:
			client.chainParams = &chaincfg.MainNetParams
		case chaincfg.TestNet3Params.Name:
			client.chainParams = &chaincfg.TestNet3Params
		case chaincfg.RegressionNetParams.Name:
			client.chainParams = &chaincfg.RegressionNetParams
		case chaincfg.SimNetParams.Name:
			client.chainParams = &chaincfg.SimNetParams
		default:
			return nil, fmt.Errorf("rpcclient.New: Unknown chain %s", config.Params)		
	}
	// attempt to start the client
	if start {
		log.Infof("Established connection to RPC server %s", config.ServerUri)
		close(connEstablished)

		// TODO implement start and fix the line below
		client.start()
		if !client.config.HTTPPostMode && !client.config.DisableAutoReconnect {
			client.wg.Add(1)
			// TODO implement wsReconnectHandler and fix the line below
			go client.wsReconnectHandler()
		}
	}
	// return the client
	return client, nil
}

// Batch is a factory that creates a client communicating with the BtcWallet Server of JSON-RPC 2.0.
// This enables the client to accept an arbitrary number of requests and have the server process them all at the same tiem.

// NOTE: this is compatible with both btcd and bitcoind
func NewBatch(config *wallet.Config) (*Client, error) {
	// check for HTTP Post Mode
	if !config.HTTPPostMode {
		// TODO handle errors
		return nil, errors.New("http post mode is required for the batch client")
	}
	// notifications are turned off since they are not supported over HTTP Post Mode
	client, err := New(config, nil)
	if err != nil {
		// TODO handle error
		return nil, err
	}
	client.batch = true // copy the client with a modified batch setting
	// start the client

	// TODO implement start and fix the line below
	client.start()

	return client, nil
}

// begin processing io
func (c *Client) start() {
	log.Tracef("Starting RPC client %s", c.config.ServerUri)
	// being processing handlers depending on the configured mode (HTTP Post or Websocket)
	if c.config.HTTPPostMode {
		c.wg.Add(1)

		// TODO implement sendPostHandler
		//go c.sendPostHandler()
	} else {
		c.wg.Add(3)
		// notification handler
		go func() {
			if c.ntfnHandlers != nil {
				if c.ntfnHandlers.OnClientConnected != nil {
					c.ntfnHandlers.OnClientConnected()
				}
			}
			c.wg.Done()
		}()
		// websocket io handlers

		// TODO implement websocket IO handlers
		go c.wsInHandler()
		go c.wsOutHandler()
	}
}

// handleMessage is the main handler for incoming notifications and responses
func (c *Client) handleMessage(msg []byte) {
	// attempt to unmarshal the message as either a notification or a response
	var in inMessage
	in.rawResponse = new(rawResponse)
	in.rawNotification = new(rawNotification)
	err := json.Unmarshal(msg, &in)

	if err != nil {
		log.Errorf("Remote server sent invalid message: %v", err)
		return
	}

	// JSON-RPC 1.0 notifications are requests with no ID
	if in.ID == nil {
		ntfn := in.rawNotification
		if ntfn == nil {
			log.Errorf("Malformed notification: missing method and parameters")
			return
		}
		if ntfn.Method == "" {
			log.Errorf("Malformed notification: missing method")
			return
		}
		// params are not optional: nil isn't valid (but len == 0 is)
		if ntfn.Params == nil {
			log.Warn("Malformed notification: missing parameters")
			return
		}
		// deliver the notification
		log.Tracef("Received notification [%s]", in.Method)
		c.handleNotification(in.rawNotification)

		return
	}
	// ensure that in.ID can be converted to an integer without loss of precision
	if *in.ID < 0 || *in.ID != math.Trunc(*in.ID) {
		log.Warn("Malformed response: invalid identifier")
		return 
	}

	if in.rawResponse == nil {
		log.Warn("Malformed response: missing result and error")
		return
	}

	id := uint64(*in.ID)
	log.Tracef("Received response for id %d (result %s)", id, in.Result)
	request := c.removeRequest(id)
	// nothing more to do if there is no request associated with this reply
	if request == nil || request.responseChan == nil {
		log.Warnf("Received unexpected reply for id %d", id)
		return
	}
	// since the command was successful, examine it to see if its a notification
	// and if it is, add it to the notification state so it can automatically be re-established on reconnect.
	c.trackRegisteredNtfns(request.cmd)
	// deliver response
	result, err := in.rawResponse.result()
	request.responseChan <- &Response{result: result, err: err}
}

// sendMessage sends the passed JSON to the connected server using the websocket connection. It is backed by a buffered channel
// so it will not block until the send channel is full.
func (c *Client) sendMessage(marshalledJSON []byte) {
	// noop if disconnected
	select {
	case c.sendChan <- marshalledJSON:
	case <- c.disconnectChan():
		return
	}
}
// Disconnect disconnects the current websocket associated with the client. The connection will automatically be re-established
// unless the client was created with the DisableAutoReconnect option.

// NOTE this function has no effect when the client is running in HTTP POST mode.
func (c *Client) Disconnect() {
	// noop if already disconnected
	if !c.doDisconnect() {
		return
	}

	c.requestLock.Lock()
	defer c.requestLock.Unlock()
	// when operating without auto reconnect, send errors to any pending requests and shutdown the client
	if c.config.DisableAutoReconnect {
		for e := c.requestList.Front(); e != nil; e = e.Next() {
			req := e.Value.(*jsonRequest)
			req.responseChan <- &Response{ result: nil, err: ErrClientDisconnect }
		}
		c.removeAllRequests()
		c.doShutdown()
	}
}

// Disconnected returns whether or not the client is currently disconnected. If
// a websocket client was created but never connected, this also returns false.
func (c *Client) Disconnected() bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	
	select {
	case <-c.connEstablished:
		return c.disconnected
	default:
		return true
	}
}

// doDisconnect disconnects the associated websocket with the client if it hasn't already been disconnected. 
// It will return false if the disconnect is not needed or the client is running in HTTP POST Mode.

// NOTE this is safe for concurrent access
func (c *Client) doDisconnect() bool {
	if c.config.HTTPPostMode {
		return false
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	// noop if already disconnected
	if c.disconnected {
		return false
	}
	log.Tracef("Disconnecting RPC client %s", c.config.ServerUri)
	close(c.disconnect)

	if c.wsConn != nil {
		c.wsConn.Close()
	}
	c.disconnected = true
	return true
}

// doShutdown closes the shutdown channel and logs the shutdown unless shutdown is already in progress.
// It will return false if the shutdown is not needed.

// NOTE this is safe for concurrent access
func (c *Client) doShutdown() bool {
	// ignore the shutdown if the client is already in the process of doing so
	select {
		case <-c.shutdown:
			return false
		default:
	}
	log.Tracef("Shutting down RPC client %s", c.config.ServerUri)

	close(c.shutdown)
	return true
}

// disconnectChan returns a copy of the current disconnect channel. The channel is read protected by the client mutex, and it is safe to call while the channel is being reassigned during a reconnect.
func (c *Client) disconnectChan() <-chan struct{} {
	c.mtx.Lock()
	ch := c.disconnect
	c.mtx.Unlock()

	return ch
}

// resendRequests resends any requests that had not completed when the client disconnected. It is intended to be called 
// once the client has reconnected as a separate goroutine.
func (c *Client) resendRequests() {
	// set the notification state back up. If anything goes wrong, 
	// disconnect the client.
	if err := c.reregisterNtfns(); err != nil {
		log.Warnf("Failed to re-register notifications: %v", err)
		c.Disconnect()
		return
	}
	// since its possible to block on send and more requests might be added by the caller,
	// make a copy of all the requests that need to be resent now and work off the copy. This also
	// allows the lock to be released quickly.
	c.requestLock.Lock()
	resendReqs := make([]*jsonRequest, 0, c.requestList.Len())
	var nextElem *list.Element
	for e := c.requestList.Front(); e != nil; e = nextElem {
		nextElem = e.Next()

		jReq := e.Value.(*jsonRequest)
		if _, ok := ignoreResends[jReq.method]; !ok {
			// if a request is not sent on reconnect, remove it from the request structures, since 
			// no reply is expected.
			delete(c.requestMap, jReq.id)
			c.requestList.Remove(e)
		} else {
			resendReqs = append(resendReqs, jReq)
		}
	}
	c.requestLock.Unlock()

	for _, jReq := range resendReqs {
		// stop resending commands if the client disconnects again
		// since the next reconnect will handle them
		if c.Disconnected() {
			return
		}
		log.Tracef("Resending command %s with id %d", jReq.method, jReq.id)
		// send message
		c.sendMessage(jReq.marshalledJSON)
	}
}

// wsReconnectHandler listens for clients disconnects and automatically tries to reconnect with retry interval that scales
// based on the number of retries. It also resends commands that had not completed when the client disconnected so the disconnect/reconnect
// process is transparent to the caller.

// NOTE this is not run whe nthe DisableAutoReconnect config option is set.
// NOTE this must be run as a goroutine
func (c *Client) wsReconnectHandler() {
out:
	for {
		select {
		case <-c.disconnect:
			// on disconnect, fallthrough  to reestablish the connection.
		case <- c.shutdown:
			break out
		}
	reconnect:
		for {
			select {
				case <-c.shutdown:
					break out
				default:
				}
			// dial websocket connection
			wsConn, err := dial(c.config)
			if err != nil {
				c.retryCount++
				log.Infof("Failed to connect to %s: %v", c.config.ServerUri, err)
				// scale the retry interval by the number of retries so there is a backoff up to a max of 1 minute.
				scaledInterval := connectionRetryInterval.Nanoseconds() * c.retryCount
				scaledDuration := time.Duration(scaledInterval)
				if scaledDuration > time.Minute {
					scaledDuration = time.Minute
				}
				log.Infof("Retrying connection to %s in "+
					"%s", c.config.ServerUri, scaledDuration)
				time.Sleep(scaledDuration)
				continue reconnect				
			}
			log.Infof("Reestablished connection to the RPC server %s", c.config.ServerUri)
			// reset the version in case the backend was disconnected due to an upgrade
			c.nodeVersionMu.Lock()
			c.nodeVersion = nil
			c.nodeVersionMu.Unlock()
			// reset the connection state and signal the reconnect has happened.
			c.mtx.Lock()
			c.wsConn = wsConn
			c.retryCount = 0

			c.disconnect = make(chan struct{})
			c.disconnected = false
			c.mtx.Unlock()
			// start the new processing input and output for the new connection
			c.start()
			// reissue pending requests in another goroutine since the send can block
			go c.resendRequests()
			// break out of the reconnect loop back to wait for disconnect again
			break reconnect
		}
	}
	c.wg.Done()
	log.Tracef("RPC client %s reconnect handler done", c.config.ServerUri)
}

// wsInHandler handles all incoming messages for the websocket connection associated with the client.
// NOTE this must be run as a goroutine
func (c *Client) wsInHandler() {
out:
	for {
		// break out of the loop once the shutdown channel has been closed.
		// Use a non-blocking select here so we fall through otherwise.
		select {
		case <-c.shutdown:
			break out
		default:
		}
		// read the message
		_, msg, err := c.wsConn.ReadMessage()
		if err != nil {
			// log the error if its not due to disconnection
			if c.shouldLogReadError(err) {
				log.Errorf("Websocket receive error from %s: %v", c.config.ServerUri, err)
			}
			break out
		}
		// handle the message
		c.handleMessage(msg)
	}
	// ensure the connection is closed
	c.Disconnect()
	c.wg.Done()
	log.Tracef("RPC client %s websocket input handler done", c.config.ServerUri)
}

// wsOutHandler handles all outgoing messages for the websocket connection. It uses a buffered channel to serialize
// output messages while allowing the sender to continue running asynchronously. 
// NOTE this must be run as a goroutine
func (c *Client) wsOutHandler() {
out:
	for {
		// send any messages ready for send until the client is disconnected or closed.
		select {
		case msg := <-c.sendChan:
			err := c.wsConn.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				c.Disconnect()
				break out
			}
		case <- c.disconnectChan():
			break out
		}
	}
	// drain any channels before exiting so nothing is left waiting around to send
cleanup:
	for {
		select {
		case <- c.sendChan:
		default:
			break cleanup
		}
	}
	c.wg.Done()
	log.Tracef("RPC client %s websocket output handler done", c.config.ServerUri)
}

// shouldLogReadError returns whether or not the passed error from the websocket should be logged. This is used to prevent spamming the logs in the case of a disconnect.
func (c *Client) shouldLogReadError(err error) bool {
	// no logging when the connection is being forcibly disconnected
	select {
	case <- c.shutdown:
		return false
	default:
	}
	// no logging when the connection has been disconnected
	if err == io.EOF {
		return false
	}
	if opErr, ok := err.(*net.OpError); ok && !opErr.Temporary() {
		return false
	}

	return true
}

// removeAllRequests removes all the jsonRequests which contain the response channels for outstanding requests.

// NOTE this must be called with the request lock held.
func (c *Client) removeAllRequests() {
	c.requestMap = make(map[uint64]*list.Element)
	c.requestList.Init()
}

// removeRequest returns and removes the jsonRequest which contains the response channel and original method associated
// with the passed ID or nil if there is no association.

// NOTE this function is safe for concurrent access.
func (c *Client) removeRequest(id uint64) *jsonRequest {
	c.requestLock.Lock()
	defer c.requestLock.Unlock()

	elem, ok := c.requestMap[id]
	if !ok {
		return nil
	}
	delete(c.requestMap, id)

	var request *jsonRequest
	if c.batch {
		request = c.batchList.Remove(elem).(*jsonRequest)
	} else {
		request = c.requestList.Remove(elem).(*jsonRequest)
	}

	return request
}
// trackRegisteredNtfns examines the passed command to see if it is one of the notification commands and updates the 
// notification state that is used to automatically re-establish registered notifications on reconnect.
func (c *Client) trackRegisteredNtfns(cmd interface{}) {
	// noop if the caller is not interested in ntfns
	if c.ntfnHandlers == nil {
		return
	}

	c.ntfnStateLock.Lock()
	defer c.ntfnStateLock.Unlock()

	switch bcmd := cmd.(type) {
	case *btcjson.NotifyBlocksCmd:
		c.ntfnState.notifyBlocks = true
	
	case *btcjson.NotifyNewTransactionsCmd:
		if bcmd.Verbose != nil && *bcmd.Verbose {
			c.ntfnState.notifyNewTxVerbose = true
		} else {
			c.ntfnState.notifyNewTx = true
		}

	case *btcjson.NotifySpentCmd:
		for _, op := range bcmd.OutPoints {
			c.ntfnState.notifySpent[op] = struct{}{}
		}
	
	case btcjson.NotifyReceivedCmd:
		for _, addr := range bcmd.Addresses {
			c.ntfnState.notifyReceived[addr] = struct{}{}
		}
	}
}

// reregisterNtfns creates and sends commands needed to re-establish the current notification state associated with the client.
// It should only be called after the reconnect by the resendRequests function
func (c *Client) reregisterNtfns() error {
	// noop if the caller is not interested in ntfns
	if c.ntfnHandlers == nil {
		return nil
	}
	// in order to avoid holding the lock on the ntfn state for the entire time of the potentially long runnning RPCs issued below,
	// make a copy of the current state and release the lock.

	// also, other commands will be running concurrently which could modify the notification state (while not under the lock)
	// which also registers it with the remote RPC server, so this prevents double registrations
	c.ntfnStateLock.Lock()
	stateCopy := c.ntfnState.Copy()
	c.ntfnStateLock.Unlock()

	// re-register notifyblocks if needed
	if stateCopy.notifyBlocks {
		log.Debugf("Re-register [notifyblocks]")
		if err := c.NotifyBlocks(); err != nil {
			return err
		}
	}

	// re-register notifynewtransactions if needed
	if stateCopy.notifyNewTx || stateCopy.notifyNewTxVerbose {
		log.Debugf("Re-register [notifynewtransactions] (verbos=%v)", stateCopy.notifyNewTxVerbose)

		err := c.NotifyNewTransactions(stateCopy.notifyNewTxVerbose)
		if err != nil {
			return err
		}
	}

	// re-register the combination of all prev registered notifyreceived addresses in one command, if needed
	nrlen := len(stateCopy.notifyReceived)
	if nrlen > 0 {
		addresses := make([]string, 0, nrlen)
		for addr := range stateCopy.notifyReceived {
			addresses = append(addresses, addr)
		}

		log.Debugf("Re-register [notifyreceived] %v", addresses)
		if err := c.notifyReceivedInternal(addresses).Receive(); err != nil {
			return err
		}
	}

	return nil
}