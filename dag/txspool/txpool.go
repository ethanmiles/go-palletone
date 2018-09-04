/*
   This file is part of go-palletone.
   go-palletone is free software: you can redistribute it and/or modify
   it under the terms of the GNU General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.
   go-palletone is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU General Public License for more details.
   You should have received a copy of the GNU General Public License
   along with go-palletone.  If not, see <http://www.gnu.org/licenses/>.
*/
/*
 * @author PalletOne core developers <dev@pallet.one>
 * @date 2018
 */

package txspool

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/palletone/go-palletone/common"
	"github.com/palletone/go-palletone/common/event"
	"github.com/palletone/go-palletone/common/log"
	"github.com/palletone/go-palletone/common/ptndb"
	"github.com/palletone/go-palletone/dag/dagconfig"
	"github.com/palletone/go-palletone/dag/modules"
	"github.com/palletone/go-palletone/dag/storage"
	"github.com/palletone/go-palletone/tokenengine/btcd/txscript"
	"gopkg.in/karalabe/cookiejar.v2/collections/prque"
)

const (
	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 10
	// rmTxChanSize is the size of channel listening to RemovedTransactionEvent.
	rmTxChanSize = 10
)

var (
	evictionInterval    = time.Minute     // Time interval to check for evictable transactions
	statsReportInterval = 8 * time.Second // Time interval to report transaction pool stats
)
var (
	// ErrInvalidSender is returned if the transaction contains an invalid signature.
	ErrInvalidSender = errors.New("invalid sender")

	// ErrNonceTooLow is returned if the nonce of a transaction is lower than the
	// one present in the local chain.
	ErrNonceTooLow = errors.New("nonce too low")

	// ErrTxFeeTooLow is returned if a transaction's tx_fee is below the value of TXFEE.
	ErrTxFeeTooLow = errors.New("txfee too low")

	// ErrUnderpriced is returned if a transaction's gas price is below the minimum
	// configured for the transaction pool.
	ErrUnderpriced = errors.New("transaction underpriced")

	// ErrReplaceUnderpriced is returned if a transaction is attempted to be replaced
	// with a different one without the required price bump.
	ErrReplaceUnderpriced = errors.New("replacement transaction underpriced")

	// ErrInsufficientFunds is returned if the total cost of executing a transaction
	// is higher than the balance of the user's account.
	ErrInsufficientFunds = errors.New("insufficient funds for gas * price + value")

	// ErrNegativeValue is a sanity error to ensure noone is able to specify a
	// transaction with a negative value.
	ErrNegativeValue = errors.New("negative value")

	// ErrOversizedData is returned if the input data of a transaction is greater
	// than some meaningful limit a user might use. This is not a consensus error
	// making the transaction invalid, rather a DOS protection.
	ErrOversizedData = errors.New("oversized data")
)

type dags interface {
	CurrentUnit() *modules.Unit
	GetUnit(hash common.Hash) *modules.Unit
	//StateAt(root common.Hash) (*state.StateDB, error)

	GetUtxoView(tx *modules.Transaction) (*UtxoViewpoint, error)
	SubscribeChainHeadEvent(ch chan<- modules.ChainHeadEvent) event.Subscription
}

// TxPoolConfig are the configuration parameters of the transaction pool.
type TxPoolConfig struct {
	NoLocals  bool          // Whether local transaction handling should be disabled
	Journal   string        // Journal of local transactions to survive node restarts
	Rejournal time.Duration // Time interval to regenerate the local transaction journal

	FeeLimit  uint64 // Minimum tx's fee  to enforce for acceptance into the pool
	PriceBump uint64 // Minimum price bump percentage to replace an already existing transaction (nonce)

	AccountSlots uint64 // Minimum number of executable transaction slots guaranteed per account
	GlobalSlots  uint64 // Maximum number of executable transaction slots for all accounts
	AccountQueue uint64 // Maximum number of non-executable transaction slots permitted per account
	GlobalQueue  uint64 // Maximum number of non-executable transaction slots for all accounts

	Lifetime time.Duration // Maximum amount of time non-executable transaction are queued
}

// DefaultTxPoolConfig contains the default configurations for the transaction
// pool.
var DefaultTxPoolConfig = TxPoolConfig{
	NoLocals:  false,
	Journal:   "transactions.rlp",
	Rejournal: time.Hour,

	FeeLimit:  1,
	PriceBump: 10,

	AccountSlots: 16,
	GlobalSlots:  4096,
	AccountQueue: 64,
	GlobalQueue:  1024,

	Lifetime: 3 * time.Hour,
}

// sanitize checks the provided user configurations and changes anything that's
// unreasonable or unworkable.
func (config *TxPoolConfig) sanitize() TxPoolConfig {
	conf := *config
	if conf.Rejournal < time.Second {
		log.Warn("Sanitizing invalid txpool journal time", "provided", conf.Rejournal, "updated", time.Second)
		conf.Rejournal = time.Second
	}
	if conf.PriceBump < 1 {
		log.Warn("Sanitizing invalid txpool price bump", "provided", conf.PriceBump, "updated", DefaultTxPoolConfig.PriceBump)
		conf.PriceBump = DefaultTxPoolConfig.PriceBump
	}
	return conf
}

type TxPool struct {
	config       TxPoolConfig
	unit         dags
	txfee        *big.Int
	txFeed       event.Feed
	scope        event.SubscriptionScope
	chainHeadCh  chan modules.ChainHeadEvent
	chainHeadSub event.Subscription
	mu           sync.RWMutex

	locals  *accountSet // Set of local transaction to exempt from eviction rules
	journal *txJournal  // Journal of local transaction to back up to disk

	beats map[common.Address]time.Time
	queue map[common.Hash]*modules.TxPoolTransaction

	pending         map[common.Hash]*modules.TxPoolTransaction // All currently processable transactions
	all             map[common.Hash]*modules.TxPoolTransaction // All transactions to allow lookups
	priority_priced *txPricedList                              // All transactions sorted by price and priority

	outpoints map[modules.OutPoint]*modules.TxPoolTransaction //

	wg sync.WaitGroup // for shutdown sync

	homestead bool
}

// NewTxPool creates a new transaction pool to gather, sort and filter inbound
// transactions from the network.
func NewTxPool(config TxPoolConfig, unit dags) *TxPool { // chainconfig *params.ChainConfig,
	// Sanitize the input to ensure no vulnerable gas prices are set
	config = (&config).sanitize()

	// Create the transaction pool with its initial settings
	pool := &TxPool{
		config:      config,
		unit:        unit,
		queue:       make(map[common.Hash]*modules.TxPoolTransaction),
		beats:       make(map[common.Address]time.Time),
		pending:     make(map[common.Hash]*modules.TxPoolTransaction),
		all:         make(map[common.Hash]*modules.TxPoolTransaction),
		chainHeadCh: make(chan modules.ChainHeadEvent, chainHeadChanSize),
		txfee:       new(big.Int).SetUint64(config.FeeLimit),
	}
	pool.locals = newAccountSet()
	pool.priority_priced = newTxPricedList(&pool.all)
	//pool.reset(nil, unit.CurrentUnit().Header())

	// If local transactions and journaling is enabled, load from disk
	if !config.NoLocals && config.Journal != "" {
		log.Info("Journal path:" + config.Journal)
		pool.journal = newTxJournal(config.Journal)

		if err := pool.journal.load(pool.AddLocal); err != nil {
			log.Warn("Failed to load transaction journal", "err", err)
		}
		if err := pool.journal.rotate(pool.local()); err != nil {
			log.Warn("Failed to rotate transaction journal", "err", err)
		}
	}
	// Subscribe events from blockchain
	pool.chainHeadSub = pool.unit.SubscribeChainHeadEvent(pool.chainHeadCh)

	// Start the event loop and return
	pool.wg.Add(1)
	go pool.loop()

	return pool
}

// loop is the transaction pool's main event loop, waiting for and reacting to
// outside blockchain events as well as for various reporting and transaction
// eviction events.
func (pool *TxPool) loop() {
	defer pool.wg.Done()

	// Start the stats reporting and transaction eviction tickers
	var prevPending, prevQueued, prevStales int

	report := time.NewTicker(statsReportInterval)
	defer report.Stop()

	evict := time.NewTicker(evictionInterval)
	defer evict.Stop()

	journal := time.NewTicker(pool.config.Rejournal)
	defer journal.Stop()

	// Track the previous head headers for transaction reorgs
	head := pool.unit.CurrentUnit()
	// head := new(modules.Unit)
	// head.UnitHeader = &modules.Header{
	// 	Creationdate: int64(1),
	// }
	// Keep waiting for and reacting to the various events
	for {
		select {
		// Handle ChainHeadEvent
		case ev := <-pool.chainHeadCh:
			if ev.Unit != nil {
				pool.mu.Lock()

				pool.reset(head.Header(), ev.Unit.Header())
				head = ev.Unit

				pool.mu.Unlock()
			}
		// Be unsubscribed due to system stopped
		//would recover
		case <-pool.chainHeadSub.Err():
			return

		// Handle stats reporting ticks
		case <-report.C:
			pool.mu.RLock()
			pending, queued := pool.stats()
			stales := pool.priority_priced.stales
			pool.mu.RUnlock()

			if pending != prevPending || queued != prevQueued || stales != prevStales {
				log.Debug("Transaction pool status report", "executable", pending, "queued", queued, "stales", stales)
				prevPending, prevQueued, prevStales = pending, queued, stales
			}

		// Handle inactive account transaction eviction
		case <-evict.C:

		// Handle local transaction journal rotation
		case <-journal.C:
			if pool.journal != nil {
				pool.mu.Lock()
				if err := pool.journal.rotate(pool.local()); err != nil {
					log.Warn("Failed to rotate local tx journal", "err", err)
				}
				pool.mu.Unlock()
			}
		}
	}
}

// reset retrieves the current state of the blockchain and ensures the content
// of the transaction pool is valid with regard to the chain state.
func (pool *TxPool) reset(oldHead, newHead *modules.Header) {

	// If we're reorging an old state, reinject all dropped transactions
	var reinject modules.Transactions

	if oldHead != nil && modules.HeaderEqual(oldHead, newHead) {
		// If the reorg is too deep, avoid doing it (will happen during fast sync)
		oldNum := oldHead.Index()
		newNum := newHead.Index()

		if depth := uint64(math.Abs(float64(oldNum) - float64(newNum))); depth > 64 {
			log.Debug("Skipping deep transaction reorg", "depth", depth)
		} else {
			// Reorg seems shallow enough to pull in all transactions into memory
			var discarded, included modules.Transactions

			var (
				rem = pool.unit.GetUnit(oldHead.Hash())
				add = pool.unit.GetUnit(newHead.Hash())
			)
			for rem.NumberU64() > add.NumberU64() {
				discarded = append(discarded, rem.Transactions()...)
				if rem = pool.unit.GetUnit(rem.ParentHash()[0]); rem == nil {
					log.Error("Unrooted old unit seen by tx pool", "block", oldHead.Number, "hash", oldHead.Hash())
					return
				}
			}
			for add.NumberU64() > rem.NumberU64() {
				included = append(included, add.Transactions()...)
				if add = pool.unit.GetUnit(add.ParentHash()[0]); add == nil {
					log.Error("Unrooted new unit seen by tx pool", "block", newHead.Number, "hash", newHead.Hash())
					return
				}
			}
			for rem.Hash() != add.Hash() {
				discarded = append(discarded, rem.Transactions()...)
				if rem = pool.unit.GetUnit(rem.ParentHash()[0]); rem == nil {
					log.Error("Unrooted old unit seen by tx pool", "block", oldHead.Number, "hash", oldHead.Hash())
					return
				}
				included = append(included, add.Transactions()...)
				if add = pool.unit.GetUnit(add.ParentHash()[0]); add == nil {
					log.Error("Unrooted new unit seen by tx pool", "block", newHead.Number, "hash", newHead.Hash())
					return
				}
			}
			reinject = modules.TxDifference(discarded, included)
		}
	}
	// Initialize the internal state to the current head
	if newHead == nil {
		newHead = pool.unit.CurrentUnit().Header() // Special case during testing
	}

	// statedb, err := pool.chain.StateAt(newHead.Root)
	// if err != nil {
	// 	log.Error("Failed to reset txpool state", "err", err)
	// 	return
	// }

	//pool.currentState = statedb
	//pool.pendingState = state.ManageState(statedb)

	// Inject any transactions discarded due to reorgs
	log.Debug("Reinjecting stale transactions", "count", len(reinject))
	pooltxs := make([]*modules.TxPoolTransaction, 0)
	for _, tx := range reinject {
		pooltxs = append(pooltxs, TxtoTxpoolTx(pool, tx))
	}

	pool.addTxsLocked(pooltxs, false)

	// validate the pool of pending transactions, this will remove
	// any transactions that have been included in the block or
	// have been invalidated because of another transaction (e.g.
	// higher gas price)
	pool.demoteUnexecutables()

	// Check the queue and move transactions over to the pending if possible
	// or remove those that have become invalid
	pool.promoteExecutables(nil)

}

// State returns the virtual managed state of the transaction pool.
// func (pool *TxPool) State() *state.ManagedState {
// 	pool.mu.RLock()
// 	defer pool.mu.RUnlock()

// 	return pool.pendingState
// }

// Stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
func (pool *TxPool) Stats() (int, int) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.stats()
}

// stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
func (pool *TxPool) stats() (int, int) {
	return len(pool.pending), len(pool.queue)
}

// Content retrieves the data content of the transaction pool, returning all the
// pending as well as queued transactions, grouped by account and sorted by nonce.
func (pool *TxPool) Content() (map[common.Hash]*modules.Transaction, map[common.Hash]*modules.Transaction) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[common.Hash]*modules.Transaction)
	queue := make(map[common.Hash]*modules.Transaction)
	for hash, tx := range pool.pending {
		pending[hash] = tx.Tx
	}
	for hash, tx := range pool.queue {
		queue[hash] = tx.Tx
	}
	return pending, queue
}

// Pending retrieves all currently processable transactions, groupped by origin
// account and sorted by priority level. The returned transaction set is a copy and can be
// freely modified by calling code.
func (pool *TxPool) Pending() (map[common.Hash]*modules.TxPoolTransaction, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[common.Hash]*modules.TxPoolTransaction)
	for addr, tx := range pool.pending {
		pending[addr] = tx
	}
	return pending, nil
}

// AllHashs returns a slice of hashes for all of the transactions in the txpool.
func (pool *TxPool) AllHashs() []*common.Hash {
	pool.mu.RLock()
	hashs := make([]*common.Hash, len(pool.all))
	i := 0
	for hash := range pool.all {
		hashcopy := hash
		hashs[i] = &hashcopy
		i++
	}
	pool.mu.RUnlock()
	return hashs
}

//
func (pool *TxPool) AllTxs() []*modules.Transaction {
	pool.mu.RLock()
	txs := make([]*modules.Transaction, len(pool.all))
	i := 0
	for _, txcopy := range pool.all {
		tx := PooltxToTx(txcopy)
		txs[i] = tx
		i++
	}
	pool.mu.RUnlock()
	return txs
}
func (pool *TxPool) Count() int {
	pool.mu.RLock()
	count := len(pool.all)
	pool.mu.RUnlock()
	return count
}

// local retrieves all currently known local transactions, groupped by origin
// account and sorted by price. The returned transaction set is a copy and can be
// freely modified by calling code.
func (pool *TxPool) local() map[common.Hash]*modules.TxPoolTransaction {
	txs := make(map[common.Hash]*modules.TxPoolTransaction)
	for hash, tx := range pool.pending {
		if tx != nil {
			txs[hash] = tx
		}
	}
	return txs
}

// validateTx checks whether a transaction is valid according to the consensus
// rules and adheres to some heuristic limits of the local node (price and size).
func (pool *TxPool) validateTx(tx *modules.TxPoolTransaction, local bool) error {
	// Don't accept the transaction if it already in the pool .
	hash := tx.Tx.Hash()
	if pool.isTransactionInPool(&hash) {
		return errors.New(fmt.Sprintf("already have transaction %v", tx.Tx.Hash()))
	}
	// 调用致远那边的校验接口， 做一次交易的校验

	// Heuristic limit, reject transactions over 32KB to prevent DOS attacks
	if tx.Tx.Size() > 32*1024 {
		return ErrOversizedData
	}

	from := modules.MsgstoAddress(tx.Tx.TxMessages[:])
	local = local || pool.locals.contains(from) // account may be local even if the transaction arrived from the network
	if !local && pool.txfee.Cmp(tx.Tx.Fee()) > 0 {
		return ErrTxFeeTooLow
	}
	// Make sure the transaction is signed properly

	// Verify crypto signatures for each input and reject the transaction if any don't verify.
	// 调用检测签名的接口 ： ValidateTransactionScripts

	return nil
}

// This function MUST be called with the txpool lock held (for reads).
func (pool *TxPool) isTransactionInPool(hash *common.Hash) bool {
	if _, exist := pool.all[*hash]; exist {
		return true
	}
	return false
}

// IsTransactionInPool returns whether or not the passed transaction already exists in the main pool.
func (pool *TxPool) IsTransactionInPool(hash *common.Hash) bool {
	pool.mu.RLock()
	inpool := pool.isTransactionInPool(hash)
	pool.mu.RUnlock()
	return inpool
}
func TxtoTxpoolTx(txpool *TxPool, tx *modules.Transaction) *modules.TxPoolTransaction {
	txpool_tx := new(modules.TxPoolTransaction)
	txpool_tx.Tx = tx

	txpool_tx.CreationDate = time.Now()
	txpool_tx.Nonce = txpool.GetNonce(tx.TxHash) + 1
	txpool_tx.Priority_lvl = txpool_tx.GetPriorityLvl()
	return txpool_tx
}

// CheckSpend checks whether the passed outpoint is already spent by a transaction in the txpool
func PooltxToTx(pooltx *modules.TxPoolTransaction) *modules.Transaction {
	return pooltx.Tx
}
func PoolTxstoTxs(pool_txs []*modules.TxPoolTransaction) []modules.Transaction {
	txs := make([]modules.Transaction, 0)
	for _, p_tx := range pool_txs {
		txs = append(txs, *p_tx.Tx)
	}
	return txs
}

func (pool *TxPool) GetNonce(hash common.Hash) uint64 {
	if tx, has := pool.all[hash]; has {
		return tx.Nonce
	}
	return 0
}

// add validates a transaction and inserts it into the non-executable queue for
// later pending promotion and execution. If the transaction is a replacement for
// an already pending or queued one, it overwrites the previous and returns this
// so outer code doesn't uselessly call promote.
//
// If a newly added transaction is marked as local, its sending account will be
// whitelisted, preventing any associated transaction from being dropped out of
// the pool due to pricing constraints.
func (pool *TxPool) add(tx *modules.TxPoolTransaction, local bool) (bool, error) {
	// If the transaction is already known, discard it
	hash := tx.Tx.Hash()

	if pool.all[hash] != nil {
		log.Trace("Discarding already known transaction", "hash", hash)
		return false, fmt.Errorf("known transaction: %x", hash)
	}

	if err := pool.checkPoolDoubleSpend(tx); err != nil {
		return false, err
	}
	// Check the transaction if it exists in the main chain and is not already fully spent.
	preout := modules.OutPoint{TxHash: hash}
	for i, msgcopy := range tx.Tx.TxMessages {
		if msgcopy.App == modules.APP_PAYMENT {
			if msg, ok := msgcopy.Payload.(modules.PaymentPayload); ok {
				for j := range msg.Output {
					preout.MessageIndex = uint32(i)
					preout.OutIndex = uint32(j)
					// get utxo entry , if the utxo entry is spent, then return  error.

				}
			}

		}
	}
	// If the transaction fails basic validation, discard it
	if err := pool.validateTx(tx, local); err != nil {
		log.Trace("Discarding invalid transaction", "hash", hash, "err", err)
		//invalidTxCounter.Inc(1)
		return false, err
	}
	// If the transaction pool is full, discard underpriced transactions
	if uint64(len(pool.all)) >= pool.config.GlobalSlots+pool.config.GlobalQueue {
		// If the new transaction is underpriced, don't accept it
		if pool.priority_priced.Underpriced(tx, pool.locals) {
			log.Trace("Discarding underpriced transaction", "hash", hash, "price", tx.Tx.Fee())
			//underpricedTxCounter.Inc(1)
			return false, ErrUnderpriced
		}
		// New transaction is better than our worse ones, make room for it
		drop := pool.priority_priced.Discard(len(pool.all)-int(pool.config.GlobalSlots+pool.config.GlobalQueue-1), pool.locals)
		for _, tx := range drop {
			log.Trace("Discarding freshly underpriced transaction", "hash", tx.Tx.TxHash, "price", tx.Tx.Fee())
			//underpricedTxCounter.Inc(1)
			pool.removeTransaction(tx.Tx, true)
		}
	}
	// If the transaction is replacing an already pending one, do directly
	//from,_ := modules.Sender(pool.signer, tx) // already validated
	from := modules.MsgstoAddress(tx.Tx.TxMessages[:])
	if list := pool.pending[tx.Tx.Hash()]; list != nil {
		// Nonce already pending, check if required price bump is met
		old := list

		// New transaction is better, replace old one
		if old != nil {
			delete(pool.all, old.Tx.Hash())
			pool.priority_priced.Removed()
			//pendingReplaceCounter.Inc(1)
		}
		return old != nil, nil
	}
	pool.all[tx.Tx.Hash()] = tx
	pool.journalTx(tx)

	// We've directly injected a replacement transaction, notify subsystems

	go pool.txFeed.Send(modules.TxPreEvent{tx.Tx})

	// New transaction isn't replacing a pending one, push into queue
	replace, err := pool.enqueueTx(hash, tx)
	if err != nil {
		return false, err
	}
	// Mark local addresses and journal local transactions
	if local {
		pool.locals.add(from)
	}
	pool.journalTx(tx)

	log.Trace("Pooled new future transaction", "hash", hash, "from", from, "repalce", replace, "err", err)
	return replace, nil
}

// enqueueTx inserts a new transaction into the non-executable transaction queue.
//
// Note, this method assumes the pool lock is held!
func (pool *TxPool) enqueueTx(hash common.Hash, tx *modules.TxPoolTransaction) (bool, error) {
	// Try to insert the transaction into the future queue

	old, ok := pool.queue[hash]
	if ok {
		// An older transaction was better, discard this
		if old.GetPriorityLvl() > tx.GetPriorityLvl() {
			return false, ErrReplaceUnderpriced
		}
		delete(pool.all, hash)
	}

	pool.all[hash] = tx
	return true, nil
}

// journalTx adds the specified transaction to the local disk journal if it is
// deemed to have been sent from a local account.
func (pool *TxPool) journalTx(tx *modules.TxPoolTransaction) {
	// Only journal if it's enabled and the transaction is local
	from := modules.MsgstoAddress(tx.Tx.TxMessages[:])
	if pool.journal == nil || !pool.locals.contains(from) {
		log.Trace("Pool journal is nil.", "journal", pool.journal.path, "locals", pool.locals.accounts)
		return
	}
	if err := pool.journal.insert(tx); err != nil {
		log.Warn("Failed to journal local transaction", "err", err)
	}
}

// promoteTx adds a transaction to the pending (processable) list of transactions.
//
// Note, this method assumes the pool lock is held!
func (pool *TxPool) promoteTx(hash common.Hash, tx *modules.TxPoolTransaction) {
	// Try to insert the transaction into the pending queue
	old := new(modules.TxPoolTransaction)
	if pool.pending[hash] != nil {
		old := pool.pending[hash]
		if old.Pending || old.Confirmed {
			// An older transaction was better, discard this
			delete(pool.all, hash)
			pool.priority_priced.Removed()
			return
		}
	}

	// Otherwise discard any previous transaction and mark this
	if old.Tx != nil {
		delete(pool.all, old.Tx.Hash())
		pool.priority_priced.Removed()
	}
	// Failsafe to work around direct pending inserts (tests)
	if pool.all[hash] == nil {
		tx.Pending = true
		pool.all[hash] = tx
		pool.pending[hash] = tx

	} else {
		tx.Pending = true
		pool.pending[hash] = tx
	}
	// Set the potentially new pending nonce and notify any subsystems of the new tx
	from := modules.MsgstoAddress(tx.Tx.TxMessages)
	pool.beats[from] = time.Now()

	go pool.txFeed.Send(modules.TxPreEvent{tx.Tx})
}

// AddLocal enqueues a single transaction into the pool if it is valid, marking
// the sender as a local one in the mean time, ensuring it goes around the local
// pricing constraints.
func (pool *TxPool) AddLocal(tx *modules.TxPoolTransaction) error {
	//tx.SetPriorityLvl(tx.GetPriorityLvl())
	return pool.addTx(tx, !pool.config.NoLocals)
}

// AddRemote enqueues a single transaction into the pool if it is valid. If the
// sender is not among the locally tracked ones, full pricing constraints will
// apply.
func (pool *TxPool) AddRemote(tx *modules.Transaction) error {
	pool_tx := TxtoTxpoolTx(pool, tx)
	return pool.addTx(pool_tx, false)
}

// AddLocals enqueues a batch of transactions into the pool if they are valid,
// marking the senders as a local ones in the mean time, ensuring they go around
// the local pricing constraints.
func (pool *TxPool) AddLocals(txs []*modules.TxPoolTransaction) []error {
	return pool.addTxs(txs, !pool.config.NoLocals)
}

// AddRemotes enqueues a batch of transactions into the pool if they are valid.
// If the senders are not among the locally tracked ones, full pricing constraints
// will apply.
func (pool *TxPool) AddRemotes(txs []*modules.Transaction) []error {
	pool_txs := make([]*modules.TxPoolTransaction, len(txs))
	for _, tx := range txs {
		pool_txs = append(pool_txs, TxtoTxpoolTx(pool, tx))
	}
	return pool.addTxs(pool_txs, false)
}

// addTx enqueues a single transaction into the pool if it is valid.
func (pool *TxPool) addTx(tx *modules.TxPoolTransaction, local bool) error {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	// Try to inject the transaction and update any state
	replace, err := pool.add(tx, local)
	if err != nil {
		return err
	}
	// If we added a new transaction, run promotion checks and return
	if !replace {
		from := modules.MsgstoAddress(tx.Tx.TxMessages[:]) // already validated
		pool.promoteExecutables([]common.Address{from})
	}
	return nil
}

// addTxs attempts to queue a batch of transactions if they are valid.
func (pool *TxPool) addTxs(txs []*modules.TxPoolTransaction, local bool) []error {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	return pool.addTxsLocked(txs, local)
}

// addTxsLocked attempts to queue a batch of transactions if they are valid,
// whilst assuming the transaction pool lock is already held.
func (pool *TxPool) addTxsLocked(txs []*modules.TxPoolTransaction, local bool) []error {
	// Add the batch of transaction, tracking the accepted ones
	dirty := make(map[common.Address]struct{})
	errs := make([]error, len(txs))
	for i, tx := range txs {
		var replace bool
		if replace, errs[i] = pool.add(tx, local); errs[i] == nil {
			if !replace {
				from := modules.MsgstoAddress(tx.Tx.TxMessages) // already validated
				dirty[from] = struct{}{}
			}
		}
	}
	// Only reprocess the internal state if something was actually added
	if len(dirty) > 0 {
		addrs := make([]common.Address, 0, len(dirty))
		for addr := range dirty {
			addrs = append(addrs, addr)
		}
		pool.promoteExecutables(addrs)
	}
	return errs
}

type TxStatus uint

const (
	TxStatusUnknown TxStatus = iota
	TxStatusQueued
	TxStatusPending
	TxStatusIncluded
)

// Status returns the status (unknown/pending/queued) of a batch of transactions
// identified by their hashes.
func (pool *TxPool) Status(hashes []common.Hash) []TxStatus {
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	status := make([]TxStatus, len(hashes))
	for i, hash := range hashes {
		if tx := pool.all[hash]; tx != nil {
			//from := modules.RSVtoAddress(tx) // already validated
			if pool.pending[tx.Tx.Hash()] != nil { //&& pool.pending[tx.TxHash].txs.items[tx.Nonce()] != nil
				status[i] = TxStatusPending
			} else {
				status[i] = TxStatusQueued
			}
		}
	}
	return status
}

// Get returns a transaction if it is contained in the pool
// and nil otherwise.
func (pool *TxPool) Get(hash common.Hash) *modules.TxPoolTransaction {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.all[hash]
}

// removeTx removes a single transaction from the queue, moving all subsequent
// transactions back to the future queue.
func (pool *TxPool) removeTx(hash common.Hash) {
	// Fetch the transaction we wish to delete
	tx, ok := pool.all[hash]
	if !ok {
		return
	}
	if tx.Tx.Hash() != hash {
		delete(pool.all, tx.Tx.Hash())
	}
	// Remove it from the list of known transactions
	delete(pool.all, hash)
	pool.priority_priced.Removed()

	// Remove the transaction from the pending lists and reset the account nonce
	if pending := pool.pending[hash]; pending != nil {
		delete(pool.pending, hash)
		from := modules.MsgstoAddress(pending.Tx.TxMessages[:])
		delete(pool.beats, from)
		// if removed, invalids := pending.Remove(tx); removed {
		// 	// If no more pending transactions are left, remove the list
		// 	if pending.Empty() {
		// 		delete(pool.pending, hash)
		// 		from := modules.MsgstoAddress(tx.TxMessages)
		// 		delete(pool.beats, from)
		// 	}
		// 	// Postpone any invalidated transactions
		// 	for _, tx := range invalids {
		// 		pool.enqueueTx(tx.TxHash, tx)
		// 	}
		// 	return
		// }
	}
}
func (pool *TxPool) RemoveTxs(hashs []common.Hash) {
	for _, hash := range hashs {
		pool.removeTx(hash)
	}
}

func (pool *TxPool) removeTransaction(tx *modules.Transaction, removeRedeemers bool) {
	hash := tx.Hash()
	if removeRedeemers {
		// Remove any transactions whitch rely on this one.
		for i, msgcopy := range tx.TxMessages {
			if msgcopy.App == modules.APP_PAYMENT {
				if msg, ok := msgcopy.Payload.(modules.PaymentPayload); ok {
					for j := uint32(0); j < uint32(len(msg.Output)); j++ {
						preout := modules.OutPoint{TxHash: hash, MessageIndex: uint32(i), OutIndex: j}
						if pooltxRedeemer, exist := pool.outpoints[preout]; exist {
							txRedeemer := PooltxToTx(pooltxRedeemer)
							pool.removeTransaction(txRedeemer, true)
						}
					}
				}
			}
		}
	}
	// Remove the transaction from the pending lists and reset the account nonce
	if pending := pool.pending[tx.Hash()]; pending != nil {
		delete(pool.pending, tx.TxHash)
		from := modules.MsgstoAddress(pending.Tx.TxMessages[:])
		delete(pool.beats, from)
	}

	// Remove the transaction if needed.
	if pooltx, exist := pool.all[hash]; exist {
		// mark the referenced outpoints as unspent by the pool.
		for _, msgcopy := range pooltx.Tx.TxMessages {
			if msgcopy.App == modules.APP_PAYMENT {
				if msg, ok := msgcopy.Payload.(modules.PaymentPayload); ok {
					for _, input := range msg.Input {
						delete(pool.outpoints, input.PreviousOutPoint)
					}
				}
			}
		}
		delete(pool.all, hash)
		pool.priority_priced.Removed()
	}
}
func (pool *TxPool) RemoveTransaction(tx *modules.Transaction, removeRedeemers bool) {
	pool.mu.Lock()
	pool.removeTransaction(tx, removeRedeemers)
	pool.mu.Unlock()
}

// RemoveDoubleSpends removes all transactions whitch spend outpoints spent by the passed
// transaction from the memory pool. Removing those transactions then leads to removing all
// transaction whitch rely on them, recursively. This is necessary when a blocks is connected
// to the main chain because the block may contain transactions whitch were previously unknow to
// the memory pool.
func (pool *TxPool) RemoveDoubleSpends(tx *modules.Transaction) {
	pool.mu.Lock()
	for _, msg := range tx.TxMessages {
		if msg.App == modules.APP_PAYMENT {
			inputs := msg.Payload.(modules.PaymentPayload)
			for _, input := range inputs.Input {
				if tx, ok := pool.outpoints[input.PreviousOutPoint]; ok {
					pool.removeTransaction(tx.Tx, true)
				}
			}
		}
	}
	pool.mu.Unlock()
}

func (pool *TxPool) checkPoolDoubleSpend(tx *modules.TxPoolTransaction) error {
	for _, msg := range tx.Tx.TxMessages {
		if msg.App == modules.APP_PAYMENT {
			var inputs modules.PaymentPayload
			inputs, ok := msg.Payload.(modules.PaymentPayload)
			if !ok {
				if err := inputs.ExtractFrInterface(msg.Payload); err != nil {
					continue
				}
			}
			for _, input := range inputs.Input {
				if tx, ok := pool.outpoints[input.PreviousOutPoint]; ok {
					str := fmt.Sprintf("output %v already spent by "+
						"transaction %v in the memory pool",
						input.PreviousOutPoint, tx.Tx.Hash())
					return errors.New(str)
				}
			}
		}
	}
	return nil
}

func (pool *TxPool) CheckSpend(output modules.OutPoint) *modules.Transaction {
	pool.mu.RLock()
	tx := pool.outpoints[output]
	pool.mu.RUnlock()
	return tx.Tx
}
func (pool *TxPool) fetchInputUtxos(tx *modules.Transaction) (*UtxoViewpoint, error) {
	utxoView, err := pool.unit.GetUtxoView(tx)
	if err != nil {
		return nil, err
	}

	// Attempt to populate any missing inputs from the transaction pool.
	for i, msgcopy := range tx.TxMessages {
		if msgcopy.App == modules.APP_PAYMENT {
			if msg, ok := msgcopy.Payload.(modules.PaymentPayload); ok {
				for _, txIn := range msg.Input {
					preout := &txIn.PreviousOutPoint
					utxo := utxoView.LookupUtxo(*preout)
					if utxo != nil {
						continue
					}
					if pooltx, exist := pool.all[preout.TxHash]; exist {
						utxoView.AddTxOut(pooltx.Tx, uint32(i), preout.OutIndex)
					}
				}
			}
		}
	}
	return utxoView, nil
}

// promoteExecutables moves transactions that have become processable from the
// future queue to the set of pending transactions. During this process, all
// invalidated transactions (low nonce, low balance) are deleted.
func (pool *TxPool) promoteExecutables(accounts []common.Address) {

	// If the pending limit is overflown, start equalizing allowances
	pending := uint64(len(pool.pending))
	if pending > pool.config.GlobalSlots {
		// Assemble a spam order to penalize large transactors first
		spammers := prque.New()
		for hash := range pool.pending {
			// Only evict transactions from high rollers
			spammers.Push(hash, float32(1))
		}
		// Gradually drop transactions from offenders
		offenders := []common.Hash{}
		for pending > pool.config.GlobalSlots && !spammers.Empty() {
			// Retrieve the next offender if not local address
			offender, _ := spammers.Pop()
			offenders = append(offenders, offender.(common.Hash))

			// Equalize balances until all the same or below threshold
			if len(offenders) > 1 {
				// Calculate the equalization threshold for all current offenders

				// Iteratively reduce all offenders until below limit or threshold reached
				for pending > pool.config.GlobalSlots {
					for i := 0; i < len(offenders)-1; i++ {
						tx := pool.pending[offenders[i]]
						// Drop the transaction from the global pools too
						hash := tx.Tx.Hash()
						delete(pool.all, hash)
						pool.priority_priced.Removed()
						log.Trace("Removed fairness-exceeding pending transaction", "hash", hash)
						pending--
					}
				}
			}
		}
		// If still above threshold, reduce to limit or min allowance
		if pending > pool.config.GlobalSlots && len(offenders) > 0 {
			for pending > pool.config.GlobalSlots {
				for _, addr := range offenders {
					tx := pool.pending[addr]
					// Drop the transaction from the global pools too
					hash := tx.Tx.Hash()
					delete(pool.all, hash)
					pool.priority_priced.Removed()
					log.Trace("Removed fairness-exceeding pending transaction", "hash", hash)
					pending--
				}
			}
		}
		//pendingRateLimitCounter.Inc(int64(pendingBeforeCap - pending))
	}

}

// demoteUnexecutables removes invalid and processed transactions from the pools
// executable/pending queue and any subsequent transactions that become unexecutable
// are moved back into the future queue.
func (pool *TxPool) demoteUnexecutables() {
	// Iterate over all accounts and demote any non-executable transactions
	for hash, tx := range pool.pending {
		// Delete the entire queue entry if it became empty.
		if tx == nil {
			delete(pool.pending, hash)
			from := modules.MsgstoAddress(tx.Tx.TxMessages[:])
			delete(pool.beats, from)
		}
	}
}

// Stop terminates the transaction pool.
func (pool *TxPool) Stop() {
	// Unsubscribe all subscriptions registered from txpool
	fmt.Println("stop start.", time.Now())
	pool.scope.Close()
	fmt.Println("scope closed.", time.Now())
	// Unsubscribe subscriptions registered from blockchain
	pool.chainHeadSub.Unsubscribe()
	pool.wg.Wait()
	fmt.Println("journal close...")
	if pool.journal != nil {
		pool.journal.close()
	}
	log.Info("Transaction pool stopped")
}

// addressByHeartbeat is an account address tagged with its last activity timestamp.
type addressByHeartbeat struct {
	address   common.Address
	heartbeat time.Time
}

type addresssByHeartbeat []addressByHeartbeat

func (a addresssByHeartbeat) Len() int           { return len(a) }
func (a addresssByHeartbeat) Less(i, j int) bool { return a[i].heartbeat.Before(a[j].heartbeat) }
func (a addresssByHeartbeat) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

/******     accountSet  *****/

// accountSet is simply a set of addresses to check for existence, and a signer
// capable of deriving addresses from transactions.
type accountSet struct {
	accounts map[common.Address]struct{}
	addrs    map[modules.OutPoint]struct{}
	//signer   modules.Signer
}

// newAccountSet creates a new address set with an associated signer for sender
// derivations.
func newAccountSet() *accountSet {
	return &accountSet{
		accounts: make(map[common.Address]struct{}),
		//signer:   signer,
	}
}

// contains checks if a given address is contained within the set.
func (as *accountSet) contains(addr common.Address) bool {
	_, exist := as.accounts[addr]
	return exist
}

// containsTx checks if the sender of a given tx is within the set. If the sender
// cannot be derived, this method returns false.
func (as *accountSet) containsTx(tx *modules.TxPoolTransaction) bool {
	// if addr, err := modules.Sender(as.signer, tx); err == nil {
	// 	return as.contains(addr)
	// }
	addr := modules.MsgstoAddress(tx.Tx.TxMessages[:])
	return as.contains(addr)
}

// add inserts a new address into the set to track.
func (as *accountSet) add(addr common.Address) {
	as.accounts[addr] = struct{}{}
}

/******  end accountSet  *****/
//  这个接口后期需要调整， 需要先将all 进行排序， 然后按序从前到后一次取出足够多tx。
func (pool *TxPool) GetSortedTxs() ([]*modules.TxPoolTransaction, common.StorageSize) {
	var list modules.TxByPriority
	var total common.StorageSize
	for _, tx := range pool.all {
		if total += tx.Tx.Size(); total <= common.StorageSize(dagconfig.DefaultConfig.UnitTxSize) {
			list = append(list, tx)
			// add  pending
			pool.promoteTx(tx.Tx.Hash(), tx)
		} else {
			total = total - tx.Tx.Size()
			break
		}
	}
	sort.Sort(list)
	return []*modules.TxPoolTransaction(list), total
}

// SubscribeTxPreEvent registers a subscription of TxPreEvent and
// starts sending event to the given channel.
func (pool *TxPool) SubscribeTxPreEvent(ch chan<- modules.TxPreEvent) event.Subscription {
	return pool.scope.Track(pool.txFeed.Subscribe(ch))
}

//  UtxoViewpoint
type UtxoViewpoint struct {
	entries  map[modules.OutPoint]*modules.Utxo
	bestHash common.Hash
}

func (view *UtxoViewpoint) BestHash() *common.Hash {
	return &view.bestHash
}
func (view *UtxoViewpoint) SetBestHash(hash *common.Hash) {
	view.bestHash = *hash
}
func (view *UtxoViewpoint) LookupUtxo(outpoint modules.OutPoint) *modules.Utxo {
	return view.entries[outpoint]
}
func (view *UtxoViewpoint) FetchUtxos(db *ptndb.Database, outpoints map[modules.OutPoint]struct{}) error {
	if len(outpoints) == 0 {
		return nil
	}
	neededSet := make(map[modules.OutPoint]struct{})
	for outpoint := range outpoints {
		if _, ok := view.entries[outpoint]; ok {
			continue
		}
		neededSet[outpoint] = struct{}{}
	}
	return view.fetchUtxosMain(db, neededSet)

}
func (view *UtxoViewpoint) fetchUtxosMain(db *ptndb.Database, outpoints map[modules.OutPoint]struct{}) error {
	if len(outpoints) == 0 {
		return nil
	}
	for outpoint := range outpoints {
		utxo, err := storage.GetUtxoEntry(*db, outpoint.ToKey())
		if err != nil {
			return err
		}
		view.entries[outpoint] = utxo
	}
	return nil
}

func (view *UtxoViewpoint) addTxOut(outpoint modules.OutPoint, txOut *modules.TxOut, isCoinbase bool) {
	// Don't add provably unspendable outputs.
	if txscript.IsUnspendable(txOut.PkScript) {
		return
	}
	utxo := view.LookupUtxo(outpoint)
	if utxo == nil {
		utxo = new(modules.Utxo)
		view.entries[outpoint] = utxo
	}
	utxo.Amount = uint64(txOut.Value)
	utxo.PkScript = txOut.PkScript
	utxo.Asset.AssertId = txOut.Asset.AssertId
	utxo.Asset.ChainId = txOut.Asset.ChainId
	utxo.Asset.UniqueId = txOut.Asset.UniqueId

	utxo.MessageIndex = outpoint.MessageIndex
	utxo.OutIndex = outpoint.OutIndex
	utxo.TxID = outpoint.TxHash
	// isCoinbase ?
	// flags --->  标记utxo状态
}

func (view *UtxoViewpoint) AddTxOut(tx *modules.Transaction, msgIdx, txoutIdx uint32) {
	if msgIdx >= uint32(len(tx.TxMessages)) {
		return
	}

	for i, msgcopy := range tx.TxMessages {

		if (uint32(i) == msgIdx) && (msgcopy.App == modules.APP_PAYMENT) {
			if msg, ok := msgcopy.Payload.(modules.PaymentPayload); ok {
				if txoutIdx >= uint32(len(msg.Output)) {
					return
				}
				preout := modules.OutPoint{TxHash: tx.Hash(), MessageIndex: msgIdx, OutIndex: txoutIdx}
				output := msg.Output[txoutIdx]
				asset := &modules.Asset{AssertId: output.Asset.AssertId, UniqueId: output.Asset.UniqueId, ChainId: output.Asset.ChainId}
				txout := &modules.TxOut{Value: int64(output.Value), PkScript: output.PkScript, Asset: *asset}
				view.addTxOut(preout, txout, false)
			}
		}

	}
}

func (view *UtxoViewpoint) AddTxOuts(tx *modules.Transaction) {
	preout := modules.OutPoint{TxHash: tx.Hash()}
	for i, msgcopy := range tx.TxMessages {
		if msgcopy.App == modules.APP_PAYMENT {
			if msg, ok := msgcopy.Payload.(modules.PaymentPayload); ok {
				msgIdx := uint32(i)
				preout.MessageIndex = msgIdx
				for j, output := range msg.Output {
					txoutIdx := uint32(j)
					preout.OutIndex = txoutIdx
					//asset := &modules.Asset{AssertId: output.Asset.AssertId, UniqueId: output.Asset.UniqueId, ChainId: output.Asset.ChainId}
					txout := &modules.TxOut{Value: int64(output.Value), PkScript: output.PkScript, Asset: modules.Asset{AssertId: output.Asset.AssertId, UniqueId: output.Asset.UniqueId, ChainId: output.Asset.ChainId}}
					view.addTxOut(preout, txout, false)
				}
			}
		}

	}
}

func (view *UtxoViewpoint) RemoveUtxo(outpoint modules.OutPoint) {
	delete(view.entries, outpoint)
}

func (view *UtxoViewpoint) Entries() map[modules.OutPoint]*modules.Utxo {
	return view.entries
}

func NewUtxoViewpoint() *UtxoViewpoint {
	return &UtxoViewpoint{
		entries: make(map[modules.OutPoint]*modules.Utxo),
	}
}
