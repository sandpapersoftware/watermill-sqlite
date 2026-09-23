package wmsqlitemodernc

import (
	"context"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/google/uuid"
)

// TestNackAfterLockIsLost makes sure that a message is still redelivered on nack
// when the consumer group lock expires while the message awaits acknowledgement.
func TestNackAfterLockIsLost(t *testing.T) {
	dsn := "file:" + uuid.New().String() + "?mode=memory&journal_mode=WAL&busy_timeout=1000&secure_delete=true&foreign_keys=true&cache=shared"
	db := newTestConnection(t, dsn)

	ctx, cancel := context.WithCancel(context.TODO()) // TODO: replace with t.Context() when Watermill bumps up to 1.24
	defer cancel()
	topic := "TestNackAfterLockIsLost"
	tg := TableNameGenerators{}.WithDefaultGeneratorsInsteadOfNils()

	pub, err := NewPublisher(db, PublisherOptions{
		InitializeSchema:    true,
		TableNameGenerators: tg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = pub.Publish(topic,
		message.NewMessage("0", []byte("payload0")),
		message.NewMessage("1", []byte("payload1")),
	); err != nil {
		t.Fatal("cannot publish the messages:", err)
	}

	noAckDeadline := time.Duration(0)
	sub, err := NewSubscriber(newTestConnection(t, dsn), SubscriberOptions{
		PollInterval:        time.Millisecond * 20,
		LockTimeout:         time.Second * 2,
		AckDeadline:         &noAckDeadline,
		InitializeSchema:    true,
		TableNameGenerators: tg,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sub.Close(); err != nil {
			t.Fatal("unable to close subscriber", err)
		}
	})
	messagesFromSubscriber, err := sub.Subscribe(ctx, topic)
	if err != nil {
		t.Fatal(err)
	}

	var msg0 *message.Message
	select {
	case msg0 = <-messagesFromSubscriber:
	case <-time.After(time.Second * 2):
		t.Fatal("timeout waiting for the first message")
	}

	// expire the lock, as if the subscriber was stalled for longer than the lock timeout
	if _, err = db.ExecContext(ctx, "UPDATE '"+tg.Offsets(topic)+"' SET locked_until=0"); err != nil {
		t.Fatal("unable to expire the lock:", err)
	}
	// wait for the lock extension attempt, which happens after 1.7 seconds
	time.Sleep(time.Millisecond * 2200)
	msg0.Nack()

	select {
	case next := <-messagesFromSubscriber:
		if next.UUID != "0" {
			t.Fatalf("expected message 0 to be redelivered after nack but got message %s", next.UUID)
		}
		next.Ack()
	case <-time.After(time.Second * 2):
		t.Fatal("timeout waiting for the redelivered message")
	}

	select {
	case next := <-messagesFromSubscriber:
		if next.UUID != "1" {
			t.Fatalf("expected message 1 but got message %s", next.UUID)
		}
		next.Ack()
	case <-time.After(time.Second * 2):
		t.Fatal("timeout waiting for the second message")
	}
}
