package raft

import (
	"sync"
)

// example RequestVote RPC arguments structure.
// field names must start with capital letters!
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

// example RequestVote RPC reply structure.
// field names must start with capital letters!
type RequestVoteReply struct {
	// Your data here (2A).
	Term        int
	VoteGranted bool
}

// 处理请求投票RPC的函数
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 如果当前任期 cur < 小于请求投票RPC的任期
	if args.Term > rf.currentTerm {
		//（节点发现其他节点有更高的任期时，它会更新自己的任期并转变为追随者。这种机制确保了任期信息在整个集群中传播，从而保持一致性。）
		rf.setNewTerm(args.Term) // 设置当前任期 cur = 投票请求RPC的任期。
	}

	// 如果当前任期 cur > 大于请求投票RPC的任期
	// 目的：保证系统中不会出现旧领导者重新成为领导者
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm // 返回当前节点的任期
		reply.VoteGranted = false   // 不投票给他
		return
	}

	// 如果任期号相同

	// 获取当前节点最新的日志
	myLastLog := rf.log.lastLog()

	// 请求投票RPC的日志是否比当前节点的日志新，两个维度（日志的任期号、日志号）
	upToDate := args.LastLogTerm > myLastLog.Term || (args.LastLogTerm == myLastLog.Term && args.LastLogIndex >= myLastLog.Index)
	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && upToDate {
		reply.VoteGranted = true
		rf.votedFor = args.CandidateId // 投票给请求投票RPC的候选者
		rf.persist()
		rf.resetElectionTimer() // 重置选举周期
		DPrintf("[%v]: term %v vote %v", rf.me, rf.currentTerm, rf.votedFor)
	} else {
		reply.VoteGranted = false // 之所以不投票给日志没当前节点新的候选者，是因为Raft算法需要保证被选出来的leader一定包含了之前各任期的所有被提交的日志条目。
	}
	reply.Term = rf.currentTerm
}

// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in 6.824/labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

func (rf *Raft) candidateRequestVote(serverId int, args *RequestVoteArgs, voteCounter *int, becomeLeader *sync.Once) {
	DPrintf("[%d]: term %v send vote request to %d\n", rf.me, args.Term, serverId)

	// 发送请求投票RPC
	reply := RequestVoteReply{}
	ok := rf.sendRequestVote(serverId, args, &reply)
	if !ok {
		return
	}

	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 如果响应的任期号大于当前机器
	if reply.Term > args.Term {
		DPrintf("[%d]: %d 在新的term，更新term，结束\n", rf.me, serverId)
		rf.setNewTerm(reply.Term)
		return
	}

	// 如果响应的任期号小于当前机器
	if reply.Term < args.Term {
		DPrintf("[%d]: %d 的term %d 已经失效，结束\n", rf.me, serverId, reply.Term)
		return
	}

	// 如果响应的任期号等于当前机器

	// 如果没有获得请求节点的投票
	if !reply.VoteGranted {
		DPrintf("[%d]: %d 没有投给me，结束\n", rf.me, serverId)
		return
	}
	DPrintf("[%d]: from %d term一致，且投给%d\n", rf.me, serverId, rf.me)

	// 得到请求节点的投票
	*voteCounter++

	// 当收到半数以上的投票则进入leader状态
	if *voteCounter > len(rf.peers)/2 && rf.currentTerm == args.Term && rf.state == Candidate {
		DPrintf("[%d]: 获得多数选票，可以提前结束\n", rf.me)
		becomeLeader.Do(func() {
			DPrintf("[%d]: 当前term %d 结束\n", rf.me, rf.currentTerm)
			rf.state = Leader
			lastLogIndex := rf.log.lastLog().Index
			for i, _ := range rf.peers {
				rf.nextIndex[i] = lastLogIndex + 1
				rf.matchIndex[i] = 0
			}
			rf.matchIndex[rf.me] = lastLogIndex
			DPrintf("[%d]: leader - nextIndex %#v", rf.me, rf.nextIndex)
			rf.appendEntries(true) // 成为leader后立刻发送一个心跳包
		})
	}
}
