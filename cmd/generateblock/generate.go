package main

import (
	"crypto/ecdsa"
	"errors"
	"github.com/AsimovNetwork/asimov/ainterface"
	"github.com/AsimovNetwork/asimov/asiutil"
	"github.com/AsimovNetwork/asimov/blockchain"
	"github.com/AsimovNetwork/asimov/chaincfg"
	"github.com/AsimovNetwork/asimov/common"
	"github.com/AsimovNetwork/asimov/crypto"
	"github.com/AsimovNetwork/asimov/mining"
	"github.com/AsimovNetwork/asimov/protos"
	"time"
)

type generateConfig struct {
	BlockTemplateGenerator *mining.BlkTmplGenerator

	ProcessBlock func(*mining.BlockTemplate, common.BehaviorFlags) (bool, error)

	IsCurrent func() bool

	ProcessSig func(*asiutil.BlockSign) error

	Chain *blockchain.BlockChain

	GasFloor uint64
	GasCeil  uint64

	RoundManager ainterface.IRoundManager

	Accounts []*crypto.Account

	StartHeight int32
}

var config *generateConfig

func generate(c *generateConfig) bool {
	config = c

	tip := config.Chain.GetTip()
	if tip.Height() < config.StartHeight {
		mainLog.Info("current tip height !=", config.StartHeight)
		return false
	}

	round := int64(tip.Round())
	slot := int64(tip.Slot())
	roundSizei64 := int64(chaincfg.ActiveNetParams.RoundSize)
	roundStartTime, err := getTargetTime(chaincfg.ActiveNetParams.ChainStartTime, 0, roundSizei64 - 1, round, 0)
	if err != nil {
		mainLog.Info("getTargetTime failed :", err.Error())
		return false
	}
	interval := config.RoundManager.GetRoundInterval(round)

	for {
		slot++
		if slot == roundSizei64 {
			slot = 0
			round++
			roundStartTime += interval
			interval = config.RoundManager.GetRoundInterval(round)
		}

		genTime := roundStartTime + interval*slot/roundSizei64 + 1
		if genTime > time.Now().Unix() {
			return true
		}

		acc := getGenAccount(slot, round)
		if acc == nil {
			mainLog.Warnf("no private key at round: %v,slot: %v", round, slot)
			continue
		}

		template, err := config.BlockTemplateGenerator.ProduceNewBlock(
			acc, config.GasFloor, config.GasCeil,
			genTime, uint32(round), uint16(slot), float64(time.Second*5/time.Millisecond))
		if err != nil {
			mainLog.Errorf("satoshiplus gen block failed to make a block: %v", err)
			return false
		}
		_, err = config.ProcessBlock(template, common.BFFastAdd)
		if err != nil {
			// Anything other than a rule violation is an unexpected error,
			// so log that error as an internal error.
			if _, ok := err.(blockchain.RuleError); !ok {
				mainLog.Errorf("Unexpected error while processing "+
					"block submitted via satoshiplus miner: %v", err)
			}
			mainLog.Errorf("satoshiplus gen block submit reject, height=%d, %v", template.Block.Height(), err)
		} else {
			mainLog.Infof("satoshiplus gen block submit accept, height=%d, hash=%v, sigNum=%v, txNum=%d",
				template.Block.Height(), template.Block.Hash(),
				len(template.Block.MsgBlock().PreBlockSigs), len(template.Block.Transactions()))
		}

		makeSignature(template.Block)
	}

	return false
}

func makeSignature(block *asiutil.Block) {
	header := &block.MsgBlock().Header

	for _,acc := range config.Accounts{
		if header.CoinBase == *acc.Address {
			continue
		}

		_, weightMap, _ := config.Chain.GetValidators(header.Round)

		if _, ok := weightMap[*acc.Address]; !ok {
			continue
		}

		blockHash := block.MsgBlock().BlockHash()

		signature, err := crypto.Sign(blockHash[:], (*ecdsa.PrivateKey)(&acc.PrivateKey))
		if err != nil {
			mainLog.Errorf("Sign error:%s.", err)
			continue
		}

		var sigMsg protos.MsgBlockSign
		copy(sigMsg.Signature[:], signature)
		sigMsg.BlockHeight = header.Height
		sigMsg.BlockHash = blockHash
		sigMsg.Signer = *acc.Address

		sig := asiutil.NewBlockSign(&sigMsg)

		config.ProcessSig(sig)
	}
}

func getGenAccount(slot, round int64) *crypto.Account {
	best := config.Chain.BestSnapshot()
	if best.Height > 0 && config.IsCurrent() != true {
		mainLog.Infof("downloading blocks: wait!!!!!!!!!!!")
		return nil
	}
	// check whether block with round/slot already exist
	node := config.Chain.GetNodeByRoundSlot(uint32(round), uint16(slot))
	if node != nil {
		return nil
	}

	validators, _, err := config.Chain.GetValidators(uint32(round))
	if err != nil {
		mainLog.Errorf("[checkTurn] %v", err.Error())
		return nil
	}

	for _, acc := range config.Accounts {
		if *validators[slot] == *acc.Address {
			return acc
		}
	}

	return nil
}

// get start time of round, it can be calculated by any older block.
func getTargetTime(blockTime int64, round int64, slot int64, targetRound int64, targetSlot int64) (int64, error) {
	if targetSlot >= int64(chaincfg.ActiveNetParams.RoundSize) {
		return 0, errors.New("slot must be less than round size")
	}

	roundInterval := config.RoundManager.GetRoundInterval(round)
	if roundInterval == 0 {
		return 0, errors.New("getRoundStartTime failed to get round interval")
	}
	if round == targetRound {
		return blockTime + roundInterval*(targetSlot-slot)/int64(chaincfg.ActiveNetParams.RoundSize), nil
	}
	roundsizei64 := int64(chaincfg.ActiveNetParams.RoundSize)

	curTime := blockTime
	//when targetRound > round
	for i := round; i <= targetRound; i++ {
		roundInterval = config.RoundManager.GetRoundInterval(i)
		if roundInterval == 0 {
			return 0, errors.New("getRoundStartTime failed to get round interval")
		}

		num := roundsizei64
		if i == round {
			num = roundsizei64 - slot
		} else if i == targetRound {
			num = targetSlot
		}
		curTime = curTime + num*roundInterval/roundsizei64
	}

	//when targetRound < round
	for i := targetRound; i <= round; i++ {
		roundInterval = config.RoundManager.GetRoundInterval(i)
		if roundInterval == 0 {
			return 0, errors.New("getRoundStartTime failed to get round interval")
		}

		num := roundsizei64
		if i == round {
			num = slot
		} else if i == targetRound {
			num = roundsizei64 - targetSlot
		}
		curTime -= num * roundInterval / roundsizei64
	}

	mainLog.Infof("satoshi get round: %d, start time %v", targetRound, curTime)
	return curTime, nil
}
