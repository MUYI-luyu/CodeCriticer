
















package cockroach16167

import (
	"sync"
	"testing"
)

type PreparedStatements struct {
	session *Session
}

func (ps PreparedStatements) New(e *Executor) {
	e.Prepare(ps.session)
}

type Session struct {
	PreparedStatements PreparedStatements
}

func (s *Session) resetForBatch(e *Executor) {
	e.getDatabaseCache()
}

type Executor struct {
	systemConfigCond *sync.Cond
	systemConfigMu   sync.RWMutex
}

func (e *Executor) Start() {
	e.updateSystemConfig()
}

func (e *Executor) execParsed(session *Session) {
	e.systemConfigCond.L.Lock()
	defer e.systemConfigCond.L.Unlock()
	runTxnAttempt(e, session)
}

func (e *Executor) execStmtsInCurrentTxn(session *Session) {
	e.execStmtInOpenTxn(session)
}

func (e *Executor) execStmtInOpenTxn(session *Session) {
	session.PreparedStatements.New(e)
}

func (e *Executor) Prepare(session *Session) {
	session.resetForBatch(e)
}

func (e *Executor) getDatabaseCache() {
	e.systemConfigMu.RLock()
	defer e.systemConfigMu.RUnlock()
}

func (e *Executor) updateSystemConfig() {
	e.systemConfigMu.Lock()
	defer e.systemConfigMu.Unlock()
}

func runTxnAttempt(e *Executor, session *Session) {
	e.execStmtsInCurrentTxn(session)
}

func NewExectorAndSession() (*Executor, *Session) {
	session := &Session{}
	session.PreparedStatements = PreparedStatements{session}
	e := &Executor{}
	return e, session
}









func TestCockroach16167(t *testing.T) {
	e, s := NewExectorAndSession()
	e.systemConfigCond = sync.NewCond(e.systemConfigMu.RLocker())
	go e.Start()
	e.execParsed(s)
}
