package sip

import (
	"fmt"
	"strings"
	"sync"
)

// TransactionManager 请求事务管理：请求发出后按 Call-ID + CSeq + Method 等待响应
type TransactionManager struct {
	mu      sync.Mutex
	pending map[string]chan *Message
}

// NewTransactionManager 创建事务管理器
func NewTransactionManager() *TransactionManager {
	return &TransactionManager{pending: map[string]chan *Message{}}
}

func txKey(callID string, cseq int, method string) string {
	return fmt.Sprintf("%s|%d|%s", callID, cseq, strings.ToUpper(method))
}

// New 注册一个待响应事务，返回响应通道
func (tm *TransactionManager) New(callID string, cseq int, method string) chan *Message {
	ch := make(chan *Message, 8)
	tm.mu.Lock()
	tm.pending[txKey(callID, cseq, method)] = ch
	tm.mu.Unlock()
	return ch
}

// Remove 移除事务
func (tm *TransactionManager) Remove(callID string, cseq int, method string) {
	tm.mu.Lock()
	delete(tm.pending, txKey(callID, cseq, method))
	tm.mu.Unlock()
}

// Handle 将响应投递给对应事务；返回是否命中
func (tm *TransactionManager) Handle(msg *Message) bool {
	if !msg.IsResponse() {
		return false
	}
	cseq, method := msg.CSeq()
	if method == "" {
		return false
	}
	key := txKey(msg.CallID(), cseq, method)
	tm.mu.Lock()
	ch, ok := tm.pending[key]
	tm.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- msg:
	default:
	}
	return true
}
