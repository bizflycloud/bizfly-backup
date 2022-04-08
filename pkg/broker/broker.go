package broker

// Broker is the interface to perform async messaging.
type Broker interface {
	Connect() error
	ConnectAndSubscribe(subHandler Handler, subTopics []string) error
	Disconnect() error
	Publish(topic string, payload interface{}) error
	Subscribe(topics []string, h Handler) error
	String() string
}

// Handler handles a message receive from a topic.
type Handler func(Event) error

// Event is the event passed to Handler
type Event struct {
	Topic     string
	Payload   []byte
	Duplicate bool
	Qos       byte
	Retained  bool
	Ack       func()
}
