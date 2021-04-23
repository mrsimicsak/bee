// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pseudosettle_test

import (
	"bytes"
	"context"
	"errors"
	"io/ioutil"
	"math/big"
	"testing"
	"time"

	"github.com/ethersphere/bee/pkg/logging"
	"github.com/ethersphere/bee/pkg/p2p/protobuf"
	"github.com/ethersphere/bee/pkg/p2p/streamtest"
	"github.com/ethersphere/bee/pkg/settlement/pseudosettle"
	"github.com/ethersphere/bee/pkg/settlement/pseudosettle/pb"
	"github.com/ethersphere/bee/pkg/statestore/mock"
	"github.com/ethersphere/bee/pkg/swarm"
)

type testObserver struct {
	receivedCalled chan notifyPaymentReceivedCall
	sentCalled     chan notifyPaymentSentCall
	peerDebts      map[string]*big.Int
}

type notifyPaymentReceivedCall struct {
	peer   swarm.Address
	amount *big.Int
}

type notifyPaymentSentCall struct {
	peer   swarm.Address
	amount *big.Int
	err    error
}

func newTestObserver(debtAmounts map[string]*big.Int) *testObserver {
	return &testObserver{
		receivedCalled: make(chan notifyPaymentReceivedCall, 1),
		sentCalled:     make(chan notifyPaymentSentCall, 1),
		peerDebts:      debtAmounts,
	}
}

func (t *testObserver) PeerDebt(peer swarm.Address) (*big.Int, error) {
	if debt, ok := t.peerDebts[peer.String()]; ok {
		return debt, nil
	}

	return nil, errors.New("Peer not listed")
}

func (t *testObserver) NotifyPaymentReceived(peer swarm.Address, amount *big.Int) error {
	t.receivedCalled <- notifyPaymentReceivedCall{
		peer:   peer,
		amount: amount,
	}
	return nil
}

func (t *testObserver) NotifyPaymentSent(peer swarm.Address, amount *big.Int, err error) {
	t.sentCalled <- notifyPaymentSentCall{
		peer:   peer,
		amount: amount,
		err:    err,
	}
}
func TestPayment(t *testing.T) {
	logger := logging.New(ioutil.Discard, 0)

	storeRecipient := mock.NewStateStore()
	defer storeRecipient.Close()

	peerID := swarm.MustParseHexAddress("9ee7add7")

	debt := int64(10000)

	observer := newTestObserver(map[string]*big.Int{peerID.String(): big.NewInt(debt)})
	recipient := pseudosettle.New(nil, logger, storeRecipient, observer)
	recipient.SetAccountingAPI(observer)

	recorder := streamtest.New(
		streamtest.WithProtocols(recipient.Protocol()),
		streamtest.WithBaseAddr(peerID),
	)

	storePayer := mock.NewStateStore()
	defer storePayer.Close()

	observer2 := newTestObserver(map[string]*big.Int{})
	payer := pseudosettle.New(recorder, logger, storePayer, observer2)
	payer.SetAccountingAPI(observer2)

	amount := big.NewInt(debt)

	payer.Pay(context.Background(), peerID, amount)

	records, err := recorder.Records(peerID, "pseudosettle", "1.0.0", "pseudosettle")
	if err != nil {
		t.Fatal(err)
	}

	if l := len(records); l != 1 {
		t.Fatalf("got %v records, want %v", l, 1)
	}

	record := records[0]

	if err := record.Err(); err != nil {
		t.Fatalf("record error: %v", err)
	}

	messages, err := protobuf.ReadMessages(
		bytes.NewReader(record.In()),
		func() protobuf.Message { return new(pb.Payment) },
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 1 {
		t.Fatalf("got %v messages, want %v", len(messages), 1)
	}

	sentAmount := big.NewInt(0).SetBytes(messages[0].(*pb.Payment).Amount)
	if sentAmount.Cmp(amount) != 0 {
		t.Fatalf("got message with amount %v, want %v", sentAmount, amount)
	}

	select {
	case call := <-observer.receivedCalled:
		if call.amount.Cmp(amount) != 0 {
			t.Fatalf("observer called with wrong amount. got %d, want %d", call.amount, amount)
		}

		if !call.peer.Equal(peerID) {
			t.Fatalf("observer called with wrong peer. got %v, want %v", call.peer, peerID)
		}

	case <-time.After(time.Second):
		t.Fatal("expected observer to be called")
	}

	select {
	case call := <-observer2.sentCalled:
		if call.amount.Cmp(amount) != 0 {
			t.Fatalf("observer called with wrong amount. got %d, want %d", call.amount, amount)
		}

		if !call.peer.Equal(peerID) {
			t.Fatalf("observer called with wrong peer. got %v, want %v", call.peer, peerID)
		}
		if call.err != nil {
			t.Fatalf("observer called with error. got %v want nil", call.err)
		}

	case <-time.After(time.Second):
		t.Fatal("expected observer to be called")
	}

	totalSent, err := payer.TotalSent(peerID)
	if err != nil {
		t.Fatal(err)
	}

	if totalSent.Cmp(sentAmount) != 0 {
		t.Fatalf("stored wrong totalSent. got %d, want %d", totalSent, sentAmount)
	}

	totalReceived, err := recipient.TotalReceived(peerID)
	if err != nil {
		t.Fatal(err)
	}

	if totalReceived.Cmp(sentAmount) != 0 {
		t.Fatalf("stored wrong totalReceived. got %d, want %d", totalReceived, sentAmount)
	}
}
