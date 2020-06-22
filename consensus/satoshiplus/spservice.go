// Copyright (c) 2018-2020. The asimov developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package satoshiplus

import (
	"errors"
	"fmt"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/consensus/params"
	"sync"
	"time"
)

// Prove of SatoshiPlus.
// using round robin algorithm to generate blocks with orders in the list of authority peers.
// this consensus depends on bitcoin's miner.
// please refer to the white paper of asimov.
type SPService struct {
	sync.Mutex
	wg         sync.WaitGroup
	existCh    chan interface{}
	context    params.Context
	blockTimer *time.Timer

	config *params.Config

	chainTipChan chan ainterface.BlockNode
	topHeight    int32
}

// Create SatoshiPlus Service
func NewSatoshiPlusService(config *params.Config) (*SPService, error) {
	if config == nil {
		return nil, errors.New("config can't be nil")
	}
	if config.Chain == nil {
		return nil, errors.New("config.chain can't be nil")
	}
	chainStartTime := chaincfg.ActiveNetParams.ChainStartTime
	roundSizei64 := int64(chaincfg.ActiveNetParams.RoundSize)
	service := &SPService{
		config:       config,
		chainTipChan: make(chan ainterface.BlockNode),
		context: params.Context{
			Round:          0,
			Slot:           roundSizei64 - 1,
			RoundStartTime: chainStartTime - (roundSizei64-1)*common.DefaultBlockInterval,
			RoundInterval:  roundSizei64 * common.DefaultBlockInterval,
			RoundSize:      roundSizei64,
		},
	}

	if !chaincfg.Cfg.DisableBlockEarly {
		config.Chain.Subscribe(service.handleBlockchainNotification)
	}
	return service, nil
}

// Start SatoshiPlus Service, it create a goroutine and check mining on blockTimer
func (s *SPService) Start() error {
	s.Lock()
	defer s.Unlock()
	if s.existCh != nil {
		return errors.New("satoshiplus consensus is already started")
	}
	if s.config.Account == nil {
		log.Warn("satoshiplus service exit when account is nil")
		return nil
	}
	log.Info("satoshiplus consensus start")

	// current block maybe do not at the best block height:
	s.blockTimer = time.NewTimer(time.Hour)
	if err := s.initializeConsensus(); err != nil {
		log.Errorf("Start satoshi service failed:", err)
		s.blockTimer.Stop()
		s.blockTimer = nil
		return err
	}

	s.existCh = make(chan interface{})
	s.wg.Add(1)
	go func() {
		existCh := s.existCh
	mainloop:
		for {
			select {
			case <-s.blockTimer.C:
				s.handleBlockTimeout()
			case chainTip := <-s.chainTipChan:
				s.handleNewBlock(chainTip)
			case <-existCh:
				break mainloop
			}
		}
		s.blockTimer.Stop()
	cleanup:
		for {
			select {
			case <-s.blockTimer.C:
			default:
				break cleanup
			}
		}
		s.wg.Done()
	}()

	return nil
}

// Halt SatoshiPlus service goroutine
func (s *SPService) Halt() error {
	s.Lock()
	defer s.Unlock()
	log.Info("satoshiplus Stop")

	if s.existCh != nil {
		close(s.existCh)
		s.existCh = nil
	}
	s.wg.Wait()
	return nil
}

// get validators for round
func (s *SPService) getValidators(round uint32, verbose bool) ([]*common.Address, map[common.Address]uint16, error) {
	validators, weightMap, err := s.config.Chain.GetValidators(round)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get validators %v", err.Error())
	}
	if int64(len(validators)) != s.context.RoundSize {
		return nil, nil, fmt.Errorf("getValidators error: can not get validators %d for round = %d",
			len(validators), round)
	}

	if verbose {
		for i := 0; i < len(validators); i++ {
			log.Infof("validators[%d]=%v", i, validators[i].String())
		}
	}
	return validators, weightMap, nil
}

// Initialize Consensus, it only called when service start
func (s *SPService) initializeConsensus() error {
	chainStartTime := chaincfg.ActiveNetParams.ChainStartTime
	now := time.Now().Unix()
	d := now - chainStartTime
	if d >= common.DefaultBlockInterval {
		round, slot, roundStartTime, err := s.getRoundInfo(now)
		if err != nil {
			log.Error("SPService initializeConsensus get round info error: ", err)
			return err
		}
		s.context.Round = round
		s.context.Slot = slot
		s.context.RoundStartTime = roundStartTime
	}

	s.resetRoundInterval()
	s.resetTimer(s.context.Slot + 1)

	log.Infof("SPService initializeConsensus round: %v, slot: %v, roundStartTime: %v", s.context.Round, s.context.Slot, s.context.RoundStartTime)
	return nil
}

// check whether it is turn to generate new block
func (s *SPService) checkTurn(slot, round int64, verbose bool) bool {
	config := s.config
	best := config.Chain.BestSnapshot()
	if best.Height > 0 && config.IsCurrent() != true {
		log.Infof("downloading blocks: wait!!!!!!!!!!!")
		return false
	}
	// check whether block with round/slot already exist
	node := config.Chain.GetNodeByRoundSlot(uint32(round), uint16(slot))
	if node != nil {
		return false
	}

	validators, _, err := s.getValidators(uint32(round), verbose)
	if err != nil {
		log.Errorf("[checkTurn] %v", err.Error())
		return false
	}

	isTurn := *validators[slot] == *s.config.Account.Address
	log.Infof("[checkTurn] slot change slot=%d, round=%d, height=%d, isTurn=%v, interval=%v",
		slot, round, best.Height+1, isTurn, s.context.RoundInterval)
	return isTurn
}

// reset round interval of context, round interval need be reset when round change.
func (s *SPService) resetRoundInterval() bool {
	roundInterval := s.config.RoundManager.GetRoundInterval(s.context.Round)
	if roundInterval == 0 {
		log.Errorf("[handleBlockTimeout] failed to adjust time")
		return false
	}
	log.Infof("Reset round interval, round %d, interval %f", s.context.Round, roundInterval)
	s.context.RoundInterval = roundInterval
	return true
}

// when the it turns to be a validator, try to generate a new block
func (s *SPService) handleBlockTimeout() {
	round, slot := s.context.Round, s.context.Slot+1
	if slot == s.context.RoundSize {
		s.context.RoundStartTime = s.context.RoundStartTime + s.context.RoundInterval
		slot = 0
		round = round + 1
	}

	s.context.Slot = slot
	if round > s.context.Round {
		s.context.Round = round
		if !s.resetRoundInterval() {
			s.blockTimer.Reset(common.DefaultBlockInterval * time.Second)
			return
		}
	}

	isTurn := s.checkTurn(slot, round, false)
	offset := s.resetTimer(s.context.Slot + 1)

	if isTurn {
		// milliseconds
		blockInterval := float64(offset / time.Millisecond)
		s.processBlock(time.Now().Unix(), round, slot, blockInterval)
		return
	}
}

func (s *SPService) handleNewBlock(chainTip ainterface.BlockNode) {
	if chainTip.Round() == uint32(s.context.Round) && chainTip.Slot() == uint16(s.context.Slot) {
		if s.blockTimer.Stop() {
			s.resetTimer(s.context.Slot)
		}
	}
}

// process block by chain
func (s *SPService) processBlock(blockTime int64, round, slot int64, interval float64) {
	log.Infof("satoshiplus gen block start at round=%d, slot=%d", round, slot)
	template, err := s.config.BlockTemplateGenerator.ProduceNewBlock(
		s.config.Account, s.config.GasFloor, s.config.GasCeil,
		blockTime, uint32(round), uint16(slot), interval)
	if err != nil {
		log.Errorf("satoshiplus gen block failed to make a block: %v", err)
		return
	}
	_, err = s.config.ProcessBlock(template, common.BFFastAdd)
	if err != nil {
		// Anything other than a rule violation is an unexpected error,
		// so log that error as an internal error.
		if _, ok := err.(blockchain.RuleError); !ok {
			log.Errorf("Unexpected error while processing "+
				"block submitted via satoshiplus miner: %v", err)
		}
		log.Errorf("satoshiplus gen block submit reject, height=%d, %v", template.Block.Height(), err)
	} else {
		log.Infof("satoshiplus gen block submit accept, height=%d, hash=%v, sigNum=%v, txNum=%d",
			template.Block.Height(), template.Block.Hash(),
			len(template.Block.MsgBlock().PreBlockSigs), len(template.Block.Transactions()))
	}
}

// return round interval currently
func (s *SPService) GetRoundInterval() int64 {
	return s.context.RoundInterval
}

// getRoundInfo returns target round/slot, caculated from passing block time/round/slot.
func (s *SPService) getRoundInfo(targetTime int64) (int64, int64, int64, error) {
	if s.context.RoundStartTime > targetTime {
		return 0, 0, 0, errors.New("targetTime must be greater than block time")
	}

	curTime := s.context.RoundStartTime
	roundInterval := s.context.RoundInterval
	round := s.context.Round
	for {
		if curTime+roundInterval >= targetTime {
			break
		}
		curTime += roundInterval
		round++
		roundInterval = s.config.RoundManager.GetRoundInterval(round)
		if roundInterval == 0 {
			err := fmt.Errorf("satoshiplus getRoundInfo failed to get round interval,round : %v", round)
			log.Errorf(err.Error())
			return 0, 0, 0, err
		}
	}

	if curTime+roundInterval == targetTime {
		return round + 1, 0, targetTime, nil
	}
	slot := (targetTime - curTime) * s.context.RoundSize / roundInterval
	return round, slot, curTime, nil
}

// reset blockTimer.
func (s *SPService) resetTimer(slot int64) time.Duration {
	d := slot * s.context.RoundInterval * int64(time.Second) / s.context.RoundSize
	offset := time.Unix(s.context.RoundStartTime, 0).Add(time.Duration(d) + time.Millisecond).Sub(time.Now())
	s.blockTimer.Reset(offset)
	return offset
}

// handleBlockchainNotification handles notifications from blockchain.  It does
// things such as generate block ahead of time.
func (s *SPService) handleBlockchainNotification(notification *blockchain.Notification) {
	if notification.Type == blockchain.NTBlockConnected {
		// A block has been connected to the main block chain.
		dataList, ok := notification.Data.([]interface{})
		if !ok || len(dataList) < 3 {
			log.Warnf("Chain notification need a block node.")
			return
		}
		node, ok := dataList[2].(ainterface.BlockNode)
		if !ok {
			log.Warnf("Chain notification need a block node at the second position.")
			return
		}
		if node.Height() <= s.topHeight {
			return
		}
		s.topHeight = node.Height()
		// clear chainTipChan if exist
		select {
		case <-s.chainTipChan:
		default:
		}
		go func() {
			s.chainTipChan <- node
		}()
	}
}
