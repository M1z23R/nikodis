package broker

type queue struct {
	items   []*Message
	maxSize int
}

func newQueue(maxSize int) *queue {
	return &queue{maxSize: maxSize}
}

func (q *queue) push(m *Message) {
	if len(q.items) >= q.maxSize {
		q.items = q.items[1:] // drop oldest
	}
	q.items = append(q.items, m)
}

func (q *queue) pop() *Message {
	if len(q.items) == 0 {
		return nil
	}
	m := q.items[0]
	q.items = q.items[1:]
	return m
}

func (q *queue) len() int {
	return len(q.items)
}
