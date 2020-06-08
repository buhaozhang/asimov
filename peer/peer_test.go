// Copyright (c) 2018-2020 The asimov developers
// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package peer_test

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/peer"
	"github.com/AsimovNetwork/asimov/protos"
	"github.com/AsimovNetwork/asimov/socks"
	"errors"
)

// conn mocks a network connection by implementing the net.Conn interface.  It
// is used to test peer connection without actually opening a network
// connection.
type conn struct {
	io.Reader
	io.Writer
	io.Closer

	// local network, address for the connection.
	lnet, laddr string

	// remote network, address for the connection.
	rnet, raddr string

	// mocks socks proxy if true
	proxy bool
}

// LocalAddr returns the local address for the connection.
func (c conn) LocalAddr() net.Addr {
	return &addr{c.lnet, c.laddr}
}

// Remote returns the remote address for the connection.
func (c conn) RemoteAddr() net.Addr {
	if !c.proxy {
		return &addr{c.rnet, c.raddr}
	}
	host, strPort, _ := net.SplitHostPort(c.raddr)
	port, _ := strconv.Atoi(strPort)
	return &socks.ProxiedAddr{
		Net:  c.rnet,
		Host: host,
		Port: port,
	}
}

// Close handles closing the connection.
func (c conn) Close() error {
	if c.Closer == nil {
		return nil
	}
	return c.Closer.Close()
}

func (c conn) SetDeadline(t time.Time) error      { return nil }
func (c conn) SetReadDeadline(t time.Time) error  { return nil }
func (c conn) SetWriteDeadline(t time.Time) error { return nil }

// addr mocks a network address
type addr struct {
	net, address string
}

func (m addr) Network() string { return m.net }
func (m addr) String() string  { return m.address }

// pipe turns two mock connections into a full-duplex connection similar to
// net.Pipe to allow pipe's with (fake) addresses.
func pipe(c1, c2 *conn) (*conn, *conn) {
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()

	c1.Writer = w1
	c1.Closer = w1
	c2.Reader = r1
	c1.Reader = r2
	c2.Writer = w2
	c2.Closer = w2

	return c1, c2
}

// peerStats holds the expected peer stats used for testing peer.
type peerStats struct {
	wantUserAgent       string
	wantServices        common.ServiceFlag
	wantProtocolVersion uint32
	wantConnected       bool
	wantVersionKnown    bool
	wantVerAckReceived  bool
	wantLastBlock       int32
	wantStartingHeight  int32
	wantLastPingTime    time.Time
	wantLastPingNonce   uint64
	wantLastPingMicros  int64
	wantTimeOffset      int64
	wantBytesSent       uint64
	wantBytesReceived   uint64
}

// testPeer tests the given peer's flags and stats
func testPeer(t *testing.T, p *peer.Peer, s peerStats) {
	if p.UserAgent() != s.wantUserAgent {
		t.Errorf("testPeer: wrong UserAgent - got %v, want %v", p.UserAgent(), s.wantUserAgent)
		return
	}

	if p.Services() != s.wantServices {
		t.Errorf("testPeer: wrong Services - got %v, want %v", p.Services(), s.wantServices)
		return
	}

	if !p.LastPingTime().Equal(s.wantLastPingTime) {
		t.Errorf("testPeer: wrong LastPingTime - got %v, want %v", p.LastPingTime(), s.wantLastPingTime)
		return
	}

	if p.LastPingNonce() != s.wantLastPingNonce {
		t.Errorf("testPeer: wrong LastPingNonce - got %v, want %v", p.LastPingNonce(), s.wantLastPingNonce)
		return
	}

	if p.LastPingMicros() != s.wantLastPingMicros {
		t.Errorf("testPeer: wrong LastPingMicros - got %v, want %v", p.LastPingMicros(), s.wantLastPingMicros)
		return
	}

	if p.VerAckReceived() != s.wantVerAckReceived {
		t.Errorf("testPeer: wrong VerAckReceived - got %v, want %v", p.VerAckReceived(), s.wantVerAckReceived)
		return
	}

	if p.VersionKnown() != s.wantVersionKnown {
		t.Errorf("testPeer: wrong VersionKnown - got %v, want %v", p.VersionKnown(), s.wantVersionKnown)
		return
	}

	if p.ProtocolVersion() != s.wantProtocolVersion {
		t.Errorf("testPeer: wrong ProtocolVersion - got %v, want %v", p.ProtocolVersion(), s.wantProtocolVersion)
		return
	}

	if p.LastBlock() != s.wantLastBlock {
		t.Errorf("testPeer: wrong LastBlock - got %v, want %v", p.LastBlock(), s.wantLastBlock)
		return
	}

	// Allow for a deviation of 1s, as the second may tick when the message is
	// in transit and the protocol doesn't support any further precision.
	if p.TimeOffset() != s.wantTimeOffset && p.TimeOffset() != s.wantTimeOffset-1 {
		t.Errorf("testPeer: wrong TimeOffset - got %v, want %v or %v", p.TimeOffset(),
			s.wantTimeOffset, s.wantTimeOffset-1)
		return
	}

	if p.BytesSent() != s.wantBytesSent {
		t.Errorf("testPeer: wrong BytesSent - got %v, want %v", p.BytesSent(), s.wantBytesSent)
		return
	}

	if p.BytesReceived() != s.wantBytesReceived {
		t.Errorf("testPeer: wrong BytesReceived - got %v, want %v", p.BytesReceived(), s.wantBytesReceived)
		return
	}

	if p.StartingHeight() != s.wantStartingHeight {
		t.Errorf("testPeer: wrong StartingHeight - got %v, want %v", p.StartingHeight(), s.wantStartingHeight)
		return
	}

	if p.Connected() != s.wantConnected {
		t.Errorf("testPeer: wrong Connected - got %v, want %v", p.Connected(), s.wantConnected)
		return
	}

	stats := p.StatsSnapshot()

	if p.ID() != stats.ID {
		t.Errorf("testPeer: wrong ID - got %v, want %v", p.ID(), stats.ID)
		return
	}

	if p.Addr() != stats.Addr {
		t.Errorf("testPeer: wrong Addr - got %v, want %v", p.Addr(), stats.Addr)
		return
	}

	if p.LastSend() != stats.LastSend {
		t.Errorf("testPeer: wrong LastSend - got %v, want %v", p.LastSend(), stats.LastSend)
		return
	}

	if p.LastRecv() != stats.LastRecv {
		t.Errorf("testPeer: wrong LastRecv - got %v, want %v", p.LastRecv(), stats.LastRecv)
		return
	}
}

// TestPeerConnection tests connection between inbound and outbound peers.
func TestPeerConnection(t *testing.T) {
	chaincfg.Cfg = &chaincfg.FConfig{}
	verack := make(chan struct{})
	peer1Cfg := &peer.Config{
		Listeners: peer.MessageListeners{
			OnVerAck: func(p *peer.Peer, msg *protos.MsgVerAck) {
				verack <- struct{}{}
			},
			OnWrite: func(p *peer.Peer, bytesWritten int, msg protos.Message,
				err error) {
				if _, ok := msg.(*protos.MsgVerAck); ok {
					verack <- struct{}{}
				}
			},
		},
		UserAgentName:     "peer",
		UserAgentVersion:  "1.0",
		UserAgentComments: []string{"comment"},
		ChainParams:       &chaincfg.MainNetParams,
		Services: 0,
	}
	peer2Cfg := &peer.Config{
		Listeners:         peer1Cfg.Listeners,
		UserAgentName:     "peer",
		UserAgentVersion:  "1.0",
		UserAgentComments: []string{"comment"},
		ChainParams:       &chaincfg.MainNetParams,
		Services:          common.SFNodeNetwork,
	}

	wantStats1 := peerStats{
		wantUserAgent: protos.DefaultUserAgent + "peer:1.0(comment)/",
		wantServices:  0,
		wantProtocolVersion: peer.MaxProtocolVersion,
		wantConnected:      true,
		wantVersionKnown:   true,
		wantVerAckReceived: true,
		wantLastPingTime:   time.Time{},
		wantLastPingNonce:  uint64(0),
		wantLastPingMicros: int64(0),
		wantTimeOffset:     int64(0),
		wantBytesSent:      191,	//  header(20 byte) + payload(171 bytes)
		wantBytesReceived:  191,
	}
	wantStats2 := peerStats{
		wantUserAgent: protos.DefaultUserAgent + "peer:1.0(comment)/",
		wantServices:  common.SFNodeNetwork,
		wantProtocolVersion: peer.MaxProtocolVersion,
		wantConnected:      true,
		wantVersionKnown:   true,
		wantVerAckReceived: true,
		wantLastPingTime:   time.Time{},
		wantLastPingNonce:  uint64(0),
		wantLastPingMicros: int64(0),
		wantTimeOffset:     int64(0),
		wantBytesSent:      191,
		wantBytesReceived:  191,
	}

	tests := []struct {
		name  string
		setup func() (*peer.Peer, *peer.Peer, error)
	}{
		{
			"basic handshake",
			func() (*peer.Peer, *peer.Peer, error) {
				inConn, outConn := pipe(
					&conn{raddr: "10.0.0.1:8333"},
					&conn{raddr: "10.0.0.2:8333"},
				)
				inPeer := peer.NewInboundPeer(peer1Cfg)
				inPeer.AssociateConnection(inConn)

				outPeer, err := peer.NewOutboundPeer(peer2Cfg, "10.0.0.2:8333")
				if err != nil {
					return nil, nil, err
				}
				outPeer.AssociateConnection(outConn)

				for i := 0; i < 4; i++ {
					select {
					case <-verack:
					case <-time.After(time.Second):
						return nil, nil, errors.New("verack timeout")
					}
				}
				return inPeer, outPeer, nil
			},
		},
		{
			"socks proxy",
			func() (*peer.Peer, *peer.Peer, error) {
				inConn, outConn := pipe(
					&conn{raddr: "10.0.0.1:8333", proxy: true},
					&conn{raddr: "10.0.0.2:8333"},
				)
				inPeer := peer.NewInboundPeer(peer1Cfg)
				inPeer.AssociateConnection(inConn)

				outPeer, err := peer.NewOutboundPeer(peer2Cfg, "10.0.0.2:8333")
				if err != nil {
					return nil, nil, err
				}
				outPeer.AssociateConnection(outConn)

				for i := 0; i < 4; i++ {
					select {
					case <-verack:
					case <-time.After(time.Second):
						return nil, nil, errors.New("verack timeout")
					}
				}
				return inPeer, outPeer, nil
			},
		},
	}
	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		t.Logf("Running the %d test", i)
		inPeer, outPeer, err := test.setup()
		if err != nil {
			t.Errorf("TestPeerConnection setup #%d: unexpected err %v", i, err)
			return
		}
		testPeer(t, inPeer, wantStats2)
		testPeer(t, outPeer, wantStats1)

		inPeer.Disconnect()
		outPeer.Disconnect()
		inPeer.WaitForDisconnect()
		outPeer.WaitForDisconnect()
	}
}

// TestPeerListeners tests that the peer listeners are called as expected.
func TestPeerListeners(t *testing.T) {
	chaincfg.Cfg = &chaincfg.FConfig{}
	verack := make(chan struct{}, 1)
	ok := make(chan protos.Message, 20)
	peerCfg := &peer.Config{
		Listeners: peer.MessageListeners{
			OnGetAddr: func(p *peer.Peer, msg *protos.MsgGetAddr) {
				ok <- msg
			},
			OnAddr: func(p *peer.Peer, msg *protos.MsgAddr) {
				ok <- msg
			},
			OnPing: func(p *peer.Peer, msg *protos.MsgPing) {
				ok <- msg
			},
			OnPong: func(p *peer.Peer, msg *protos.MsgPong) {
				ok <- msg
			},
			OnMemPool: func(p *peer.Peer, msg *protos.MsgMemPool) {
				ok <- msg
			},
			OnTx: func(p *peer.Peer, msg *protos.MsgTx) {
				ok <- msg
			},
			OnBlock: func(p *peer.Peer, msg *protos.MsgBlock, buf []byte) {
				ok <- msg
			},
			OnInv: func(p *peer.Peer, msg *protos.MsgInv) {
				ok <- msg
			},
			OnHeaders: func(p *peer.Peer, msg *protos.MsgHeaders) {
				ok <- msg
			},
			OnNotFound: func(p *peer.Peer, msg *protos.MsgNotFound) {
				ok <- msg
			},
			OnGetData: func(p *peer.Peer, msg *protos.MsgGetData) {
				ok <- msg
			},
			OnGetBlocks: func(p *peer.Peer, msg *protos.MsgGetBlocks) {
				ok <- msg
			},
			OnGetHeaders: func(p *peer.Peer, msg *protos.MsgGetHeaders) {
				ok <- msg
			},
			OnGetCFilters: func(p *peer.Peer, msg *protos.MsgGetCFilters) {
				ok <- msg
			},
			OnGetCFHeaders: func(p *peer.Peer, msg *protos.MsgGetCFHeaders) {
				ok <- msg
			},
			OnGetCFCheckpt: func(p *peer.Peer, msg *protos.MsgGetCFCheckpt) {
				ok <- msg
			},
			OnCFilter: func(p *peer.Peer, msg *protos.MsgCFilter) {
				ok <- msg
			},
			OnCFHeaders: func(p *peer.Peer, msg *protos.MsgCFHeaders) {
				ok <- msg
			},
			OnFeeFilter: func(p *peer.Peer, msg *protos.MsgFeeFilter) {
				ok <- msg
			},
			OnFilterAdd: func(p *peer.Peer, msg *protos.MsgFilterAdd) {
				ok <- msg
			},
			OnFilterClear: func(p *peer.Peer, msg *protos.MsgFilterClear) {
				ok <- msg
			},
			OnFilterLoad: func(p *peer.Peer, msg *protos.MsgFilterLoad) {
				ok <- msg
			},
			OnMerkleBlock: func(p *peer.Peer, msg *protos.MsgMerkleBlock) {
				ok <- msg
			},
			OnVersion: func(p *peer.Peer, msg *protos.MsgVersion) *protos.MsgReject {
				ok <- msg
				return nil
			},
			OnVerAck: func(p *peer.Peer, msg *protos.MsgVerAck) {
				verack <- struct{}{}
			},
			OnReject: func(p *peer.Peer, msg *protos.MsgReject) {
				ok <- msg
			},
			OnSendHeaders: func(p *peer.Peer, msg *protos.MsgSendHeaders) {
				ok <- msg
			},
		},
		UserAgentName:     "peer",
		UserAgentVersion:  "1.0",
		UserAgentComments: []string{"comment"},
		ChainParams:       &chaincfg.MainNetParams,
	}
	inConn, outConn := pipe(
		&conn{raddr: "10.0.0.1:8333"},
		&conn{raddr: "10.0.0.2:8333"},
	)
	inPeer := peer.NewInboundPeer(peerCfg)
	inPeer.AssociateConnection(inConn)

	peerCfg.Listeners = peer.MessageListeners{
		OnVerAck: func(p *peer.Peer, msg *protos.MsgVerAck) {
			verack <- struct{}{}
		},
	}
	outPeer, err := peer.NewOutboundPeer(peerCfg, "10.0.0.1:8333")
	if err != nil {
		t.Errorf("NewOutboundPeer: unexpected err %v\n", err)
		return
	}
	outPeer.AssociateConnection(outConn)

	for i := 0; i < 2; i++ {
		select {
		case <-verack:
		case <-time.After(time.Second * 1):
			t.Errorf("TestPeerListeners: verack timeout\n")
			return
		}
	}

	tests := []struct {
		listener string
		msg      protos.Message
	}{
		{
			"OnGetAddr",
			protos.NewMsgGetAddr(),
		},
		{
			"OnAddr",
			protos.NewMsgAddr(),
		},
		{
			"OnPing",
			protos.NewMsgPing(42),
		},
		{
			"OnPong",
			protos.NewMsgPong(42),
		},
		{
			"OnMemPool",
			protos.NewMsgMemPool(),
		},
		{
			"OnTx",
			protos.NewMsgTx(protos.TxVersion),
		},
		{
			"OnInv",
			protos.NewMsgInv(),
		},
		{
			"OnHeaders",
			protos.NewMsgHeaders(),
		},
		{
			"OnNotFound",
			protos.NewMsgNotFound(),
		},
		{
			"OnGetData",
			protos.NewMsgGetData(),
		},
		{
			"OnGetBlocks",
			protos.NewMsgGetBlocks(&common.Hash{}),
		},
		{
			"OnGetHeaders",
			protos.NewMsgGetHeaders(),
		},
		{
			"OnGetCFilters",
			protos.NewMsgGetCFilters(protos.GCSFilterRegular, 0, &common.Hash{}),
		},
		{
			"OnGetCFHeaders",
			protos.NewMsgGetCFHeaders(protos.GCSFilterRegular, 0, &common.Hash{}),
		},
		{
			"OnGetCFCheckpt",
			protos.NewMsgGetCFCheckpt(protos.GCSFilterRegular, &common.Hash{}),
		},
		{
			"OnCFilter",
			protos.NewMsgCFilter(protos.GCSFilterRegular, &common.Hash{},
				[]byte("payload")),
		},
		{
			"OnCFHeaders",
			protos.NewMsgCFHeaders(),
		},
		{
			"OnFeeFilter",
			protos.NewMsgFeeFilter(15000),
		},
		{
			"OnFilterAdd",
			protos.NewMsgFilterAdd([]byte{0x01}),
		},
		{
			"OnFilterClear",
			protos.NewMsgFilterClear(),
		},
		{
			"OnFilterLoad",
			protos.NewMsgFilterLoad([]byte{0x01}, 10, 0, protos.BloomUpdateNone),
		},
		{
			"OnMerkleBlock",
			protos.NewMsgMerkleBlock(protos.NewBlockHeader(1,
				&common.Hash{})),
		},
		{
			"OnReject",
			protos.NewMsgReject("block", protos.RejectDuplicate, "dupe block"),
		},
		{
			"OnSendHeaders",
			protos.NewMsgSendHeaders(),
		},
	}
	t.Logf("Running %d tests", len(tests))
	for _, test := range tests {
		// Queue the test message
		outPeer.QueueMessage(test.msg, nil)
		select {
		case <-ok:
		case <-time.After(time.Second * 1):
			t.Errorf("TestPeerListeners: %s timeout", test.listener)
			return
		}
	}
	inPeer.Disconnect()
	outPeer.Disconnect()
}

// TestOutboundPeer tests that the outbound peer works as expected.
func TestOutboundPeer(t *testing.T) {
	chaincfg.Cfg = &chaincfg.FConfig{}
	peerCfg := &peer.Config{
		NewestBlock: func() (*common.Hash, int32, uint64, error) {
			return nil, 0, 0, errors.New("newest block not found")
		},
		UserAgentName:     "peer",
		UserAgentVersion:  "1.0",
		UserAgentComments: []string{"comment"},
		ChainParams:       &chaincfg.MainNetParams,
		Services:          0,
	}

	r, w := io.Pipe()
	c := &conn{raddr: "10.0.0.1:8333", Writer: w, Reader: r}

	p, err := peer.NewOutboundPeer(peerCfg, "10.0.0.1:8333")
	if err != nil {
		t.Errorf("NewOutboundPeer: unexpected err - %v\n", err)
		return
	}

	// Test trying to connect twice.
	p.AssociateConnection(c)
	p.AssociateConnection(c)

	disconnected := make(chan struct{})
	go func() {
		p.WaitForDisconnect()
		disconnected <- struct{}{}
	}()

	select {
	case <-disconnected:
		close(disconnected)
	case <-time.After(time.Second):
		t.Fatal("Peer did not automatically disconnect.")
	}

	if p.Connected() {
		t.Fatalf("Should not be connected as NewestBlock produces error.")
	}

	// Test Queue Inv
	fakeBlockHash := &common.Hash{0: 0x00, 1: 0x01}
	fakeInv := protos.NewInvVect(protos.InvTypeBlock, fakeBlockHash)

	// Should be noops as the peer could not connect.
	p.QueueInventory(fakeInv)
	p.AddKnownInventory(fakeInv)
	p.QueueInventory(fakeInv)

	fakeMsg := protos.NewMsgVerAck()
	p.QueueMessage(fakeMsg, nil)
	done := make(chan struct{})
	p.QueueMessage(fakeMsg, done)
	<-done
	p.Disconnect()

	// Test NewestBlock
	var newestBlock = func() (*common.Hash, int32, uint64, error) {
		hashStr := "14a0810ac680a3eb3f82edc878cea25ec41d6b790744e5daeef"
		hash, err := common.NewHashFromStr(hashStr)
		if err != nil {
			return nil, 0, 0, err
		}
		return hash, 234439, 0, nil
	}

	peerCfg.NewestBlock = newestBlock
	r1, w1 := io.Pipe()
	c1 := &conn{raddr: "10.0.0.1:8333", Writer: w1, Reader: r1}
	p1, err := peer.NewOutboundPeer(peerCfg, "10.0.0.1:8333")
	if err != nil {
		t.Errorf("NewOutboundPeer: unexpected err - %v\n", err)
		return
	}
	p1.AssociateConnection(c1)

	// Test update latest block
	latestBlockHash, err := common.NewHashFromStr("1a63f9cdff1752e6375c8c76e543a71d239e1a2e5c6db1aa679")
	if err != nil {
		t.Errorf("NewHashFromStr: unexpected err %v\n", err)
		return
	}
	p1.UpdateLastAnnouncedBlock(latestBlockHash)
	p1.UpdateLastBlockHeight(234440)
	if p1.LastAnnouncedBlock() != latestBlockHash {
		t.Errorf("LastAnnouncedBlock: wrong block - got %v, want %v",
			p1.LastAnnouncedBlock(), latestBlockHash)
		return
	}

	// Test Queue Inv after connection
	p1.QueueInventory(fakeInv)
	p1.Disconnect()

	// Test regression
	peerCfg.ChainParams = &chaincfg.DevelopNetParams

	r2, w2 := io.Pipe()
	c2 := &conn{raddr: "10.0.0.1:8333", Writer: w2, Reader: r2}
	p2, err := peer.NewOutboundPeer(peerCfg, "10.0.0.1:8333")
	if err != nil {
		t.Errorf("NewOutboundPeer: unexpected err - %v\n", err)
		return
	}
	p2.AssociateConnection(c2)

	// Test PushXXX
	var addrs []*protos.NetAddress
	for i := 0; i < 5; i++ {
		na := protos.NetAddress{}
		addrs = append(addrs, &na)
	}
	if _, err := p2.PushAddrMsg(addrs); err != nil {
		t.Errorf("PushAddrMsg: unexpected err %v\n", err)
		return
	}
	if err := p2.PushGetBlocksMsg(nil, &common.Hash{}); err != nil {
		t.Errorf("PushGetBlocksMsg: unexpected err %v\n", err)
		return
	}
	if err := p2.PushGetHeadersMsg(nil, &common.Hash{}); err != nil {
		t.Errorf("PushGetHeadersMsg: unexpected err %v\n", err)
		return
	}

	p2.PushRejectMsg("block", protos.RejectMalformed, "malformed", &common.Hash{}, false)
	p2.PushRejectMsg("block", protos.RejectInvalid, "invalid", &common.Hash{}, false)

	// Test Queue Messages
	p2.QueueMessage(protos.NewMsgGetAddr(), nil)
	p2.QueueMessage(protos.NewMsgPing(1), nil)
	p2.QueueMessage(protos.NewMsgMemPool(), nil)
	p2.QueueMessage(protos.NewMsgGetData(), nil)
	p2.QueueMessage(protos.NewMsgGetHeaders(), nil)
	p2.QueueMessage(protos.NewMsgFeeFilter(20000), nil)

	p2.Disconnect()
}

// Tests that the node disconnects from peers with an unsupported protocol
// version.
func TestUnsupportedVersionPeer(t *testing.T) {
	chaincfg.Cfg = &chaincfg.FConfig{}
	peerCfg := &peer.Config{
		UserAgentName:     "peer",
		UserAgentVersion:  "1.0",
		UserAgentComments: []string{"comment"},
		ChainParams:       &chaincfg.MainNetParams,
		Services:          0,
	}

	localNA := protos.NewNetAddressIPPort(
		net.ParseIP("10.0.0.1"),
		uint16(8333),
		common.SFNodeNetwork,
	)
	remoteNA := protos.NewNetAddressIPPort(
		net.ParseIP("10.0.0.2"),
		uint16(8333),
		common.SFNodeNetwork,
	)
	localConn, remoteConn := pipe(
		&conn{laddr: "10.0.0.1:8333", raddr: "10.0.0.2:8333"},
		&conn{laddr: "10.0.0.2:8333", raddr: "10.0.0.1:8333"},
	)

	p, err := peer.NewOutboundPeer(peerCfg, "10.0.0.1:8333")
	if err != nil {
		t.Fatalf("NewOutboundPeer: unexpected err - %v\n", err)
	}
	p.AssociateConnection(localConn)

	// Read outbound messages to peer into a channel
	outboundMessages := make(chan protos.Message)
	go func() {
		for {
			_, msg, _, err := protos.ReadMessageN(
				remoteConn,
				p.ProtocolVersion(),
			)
			if err == io.EOF {
				close(outboundMessages)
				return
			}
			if err != nil {
				t.Errorf("Error reading message from local node: %v\n", err)
				return
			}

			outboundMessages <- msg
		}
	}()

	// Read version message sent to remote peer
	select {
	case msg := <-outboundMessages:
		if _, ok := msg.(*protos.MsgVersion); !ok {
			t.Fatalf("Expected version message, got [%s]", msg.Command())
		}
	case <-time.After(time.Second):
		t.Fatal("Peer did not send version message")
	}

	// Remote peer writes version message advertising invalid protocol version 0
	invalidVersionMsg := protos.NewMsgVersion(remoteNA, localNA, 0, 0, 0)
	invalidVersionMsg.ProtocolVersion = 0

	_, err = protos.WriteMessageN(
		remoteConn.Writer,
		invalidVersionMsg,
		uint32(invalidVersionMsg.ProtocolVersion),
	)
	if err != nil {
		t.Fatalf("protos.WriteMessageN: unexpected err - %v\n", err)
	}

	// Expect peer to disconnect automatically
	disconnected := make(chan struct{})
	go func() {
		p.WaitForDisconnect()
		disconnected <- struct{}{}
	}()

	select {
	case <-disconnected:
		close(disconnected)
	case <-time.After(time.Second):
		t.Fatal("Peer did not automatically disconnect")
	}

	// Expect no further outbound messages from peer
	select {
	case msg, chanOpen := <-outboundMessages:
		if chanOpen {
			t.Fatalf("Expected no further messages, received [%s]", msg.Command())
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for remote reader to close")
	}
}
// TestDuplicateVersionMsg ensures that receiving a version message after one
// has already been received results in the peer being disconnected.
func TestDuplicateVersionMsg(t *testing.T) {
	chaincfg.Cfg = &chaincfg.FConfig{}
    // Create a pair of peers that are connected to each other using a fake
    // connection.
    verack := make(chan struct{})
    peerCfg := &peer.Config{
	            Listeners: peer.MessageListeners{
	            	OnVerAck: func(p *peer.Peer, msg *protos.MsgVerAck) {
	            		verack <- struct{}{}
	            		},
		            },
	            UserAgentName:    "peer",
	            UserAgentVersion: "1.0",
	            ChainParams:      &chaincfg.MainNetParams,
	            Services:         0,
	    }
    inConn, outConn := pipe(
	            &conn{laddr: "10.0.0.1:9108", raddr: "10.0.0.2:9108"},
	            &conn{laddr: "10.0.0.2:9108", raddr: "10.0.0.1:9108"},
	    )
    outPeer, err := peer.NewOutboundPeer(peerCfg, inConn.laddr)
    if err != nil {
    	t.Fatalf("NewOutboundPeer: unexpected err: %v\n", err)
    }
    outPeer.AssociateConnection(outConn)
    inPeer := peer.NewInboundPeer(peerCfg)
    inPeer.AssociateConnection(inConn)
    // Wait for the veracks from the initial protocol version negotiation.
	for i := 0; i < 2; i++ {
		select {
		case <-verack:
		case <-time.After(time.Second):
			t.Fatal("verack timeout")
		}
	}
    // Queue a duplicate version message from the outbound peer and wait until
    // it is sent.
    done := make(chan struct{})
    outPeer.QueueMessage(&protos.MsgVersion{}, done)
	select {
	case <-done:
	case <-time.After(time.Second):
			t.Fatal("send duplicate version timeout")
	}
    // Ensure the peer that is the recipient of the duplicate version closes the
    // connection.
	disconnected := make(chan struct{}, 1)
    go func() {
    	inPeer.WaitForDisconnect()
    	disconnected <- struct{}{}
	}()
    select {
	case <-disconnected:
	case <-time.After(time.Second):
			t.Fatal("peer did not disconnect")
	}
}

func init() {
	// Allow self connection when running the tests.
	peer.TstAllowSelfConns()
}
