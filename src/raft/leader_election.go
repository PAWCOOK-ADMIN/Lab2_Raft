package raft

import (
	"math/rand"
	"sync"
	"time"
)

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	term := rf.currentTerm
	isleader := rf.state == Leader
	return term, isleader
}

func (rf *Raft) resetElectionTimer() {
	t := time.Now()
	electionTimeout := time.Duration(75+rand.Intn(100)) * time.Millisecond // 3-6个心跳间隔
	rf.electionTime = t.Add(electionTimeout)
}

func (rf *Raft) setNewTerm(term int) {
	if term > rf.currentTerm || rf.currentTerm == 0 {
		rf.state = Follower   // 设置当前的节点为Follower
		rf.currentTerm = term // 设置当前的任期为term
		rf.votedFor = -1
		DPrintf("[%d]: set term %v\n", rf.me, rf.currentTerm)
		rf.persist() // 持久化
	}
}

func (rf *Raft) leaderElection() {
	rf.currentTerm++     // 任期号+1
	rf.state = Candidate // 首先将自己的状态设置为 Candidate
	rf.votedFor = rf.me  // 给自己投上一票
	rf.persist()
	rf.resetElectionTimer() // 重置选举超时时间
	term := rf.currentTerm
	voteCounter := 1
	lastLog := rf.log.lastLog()
	DPrintf("[%v]: start leader election, term %d\n", rf.me, rf.currentTerm)
	args := RequestVoteArgs{
		Term:         term,
		CandidateId:  rf.me,
		LastLogIndex: lastLog.Index, // 当前节点上最新日志的index
		LastLogTerm:  lastLog.Term,  // 当前节点上最新日志的任期
	}

	var becomeLeader sync.Once
	for serverId, _ := range rf.peers { // 给所有其他节点发送请求投票RPC
		if serverId != rf.me {
			go rf.candidateRequestVote(serverId, &args, &voteCounter, &becomeLeader)
		}
	}
}
