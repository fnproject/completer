package persistence

/**
  This is derived from vendor/github.com/AsynkronIT/protoactor-go/persistence/persistence_provider .go
  This has been modified to support propagating event indices to plugins
*/
import (
	fmt "fmt"

	proto "github.com/golang/protobuf/proto"
)

//Provider is the abstraction used for persistence
type Provider interface {
	GetState() ProviderState
}

// ProviderState is the contract with a given persistence provider
type ProviderState interface {
	Restart()
	GetSnapshotInterval() int
	GetSnapshot(actorName string) (snapshot interface{}, eventIndex int, ok bool)
	GetEvents(actorName string, eventIndexStart int, callback func(messageIndex int, e interface{}))
	PersistEvent(actorName string, eventIndex int, event proto.Message)
	PersistSnapshot(actorName string, eventIndex int, snapshot proto.Message)
}

// ErrMarshalling will be provided to panic on marshalling failures
var ErrMarshalling = fmt.Errorf("Persistence provider failed with marshalling error")

// ErrPersistenceFailed is the panic reason if PersistEvent fails to write to persistence provider
var ErrPersistenceFailed = fmt.Errorf("Persistence provider failed to persist event")

// ErrReadingEvents is the panic reason if GetEvents fails to read from persistence provider
var ErrReadingEvents = fmt.Errorf("Persistence provider failed to read events")

// ErrPersistingSnapshot will be provided to panic on PersistSnapshot failures
var ErrPersistingSnapshot = fmt.Errorf("Persistence provider failed to persist snapshot")
