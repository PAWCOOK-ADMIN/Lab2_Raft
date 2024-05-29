package raft

type AppendEntriesArgs struct {
	Term         int
	LeaderId     int
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []Entry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term     int
	Success  bool
	Conflict bool // true, 则说明日志缺失
	XTerm    int  // 冲突日志的任期
	XIndex   int  //
	XLen     int  // 节点的日志长度
}

// 发送追加条目RPC，用于日志复制和发送心跳
func (rf *Raft) appendEntries(heartbeat bool) {
	lastLog := rf.log.lastLog() // 获取最新的一条日志

	// 遍历集群中的每个节点
	for peer, _ := range rf.peers {
		if peer == rf.me { // 如果是当前节点
			rf.resetElectionTimer() // 重置electionTime，并跳过
			continue
		}
		// rules for leader 3
		if lastLog.Index >= rf.nextIndex[peer] || heartbeat {
			nextIndex := rf.nextIndex[peer]
			if nextIndex <= 0 {
				nextIndex = 1
			}
			if lastLog.Index+1 < nextIndex {
				nextIndex = lastLog.Index
			}
			prevLog := rf.log.at(nextIndex - 1)
			args := AppendEntriesArgs{
				Term:         rf.currentTerm,                           // 任期
				LeaderId:     rf.me,                                    // leaderID，集群节点数据的下标
				PrevLogIndex: prevLog.Index,                            // 上一个日志的index
				PrevLogTerm:  prevLog.Term,                             // 上一个日志的任期
				Entries:      make([]Entry, lastLog.Index-nextIndex+1), // 所有新的日志
				LeaderCommit: rf.commitIndex,
			}
			copy(args.Entries, rf.log.slice(nextIndex)) // 填充日志
			go rf.leaderSendEntries(peer, &args)        // 给节点发送追加条目RPC
		}
	}
}

// 发送追加条目RPC（日志复制或者发送心跳）
func (rf *Raft) leaderSendEntries(serverId int, args *AppendEntriesArgs) {
	var reply AppendEntriesReply
	ok := rf.sendAppendEntries(serverId, args, &reply)
	if !ok {
		return
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 如果响应的任期大于leader节点的任期
	if reply.Term > rf.currentTerm {
		rf.setNewTerm(reply.Term)
		return
	}

	if args.Term == rf.currentTerm {
		// rules for leader 3.1
		if reply.Success {
			match := args.PrevLogIndex + len(args.Entries)
			next := match + 1
			rf.nextIndex[serverId] = max(rf.nextIndex[serverId], next) // 根据响应更新 nextIndex 和 matchIndex
			rf.matchIndex[serverId] = max(rf.matchIndex[serverId], match)
			DPrintf("[%v]: %v append success next %v match %v", rf.me, serverId, rf.nextIndex[serverId], rf.matchIndex[serverId])
		} else if reply.Conflict {
			DPrintf("[%v]: Conflict from %v %#v", rf.me, serverId, reply)
			if reply.XTerm == -1 {
				rf.nextIndex[serverId] = reply.XLen
			} else {
				lastLogInXTerm := rf.findLastLogInTerm(reply.XTerm)
				DPrintf("[%v]: lastLogInXTerm %v", rf.me, lastLogInXTerm)
				if lastLogInXTerm > 0 {
					rf.nextIndex[serverId] = lastLogInXTerm
				} else {
					rf.nextIndex[serverId] = reply.XIndex
				}
			}

			DPrintf("[%v]: leader nextIndex[%v] %v", rf.me, serverId, rf.nextIndex[serverId])
		} else if rf.nextIndex[serverId] > 1 {
			rf.nextIndex[serverId]--
		}
		rf.leaderCommitRule()
	}
}

func (rf *Raft) findLastLogInTerm(x int) int {
	for i := rf.log.lastLog().Index; i > 0; i-- {
		term := rf.log.at(i).Term
		if term == x {
			return i
		} else if term < x {
			break
		}
	}
	return -1
}

func (rf *Raft) leaderCommitRule() {
	// leader rule 4
	if rf.state != Leader {
		return
	}

	for n := rf.commitIndex + 1; n <= rf.log.lastLog().Index; n++ {
		if rf.log.at(n).Term != rf.currentTerm {
			continue
		}
		counter := 1
		for serverId := 0; serverId < len(rf.peers); serverId++ {
			if serverId != rf.me && rf.matchIndex[serverId] >= n {
				counter++
			}
			if counter > len(rf.peers)/2 {
				rf.commitIndex = n
				DPrintf("[%v] leader尝试提交 index %v", rf.me, rf.commitIndex)
				rf.apply()
				break
			}
		}
	}
}

// 　追加条目 RPC，日志复制
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	DPrintf("[%d]: (term %d) follower 收到 [%v] AppendEntries %v, prevIndex %v, prevTerm %v", rf.me, rf.currentTerm, args.LeaderId, args.Entries, args.PrevLogIndex, args.PrevLogTerm)

	// 处理请求并初始化回复
	reply.Success = false
	reply.Term = rf.currentTerm

	// 如果请求中的任期大于当前任期，更新当前任期并转换为跟随者
	if args.Term > rf.currentTerm {
		rf.setNewTerm(args.Term)
		return // 为什么不更新超时时间再返回呢？
	}

	// 如果请求中的任期小于当前任期，直接返回
	if args.Term < rf.currentTerm {
		return
	}

	// 重置选举超时
	rf.resetElectionTimer()

	// 如果当前节点是候选人，转换为跟随者
	if rf.state == Candidate {
		rf.state = Follower
	}

	// 检查日志是否与前一个日志条目匹配
	if rf.log.lastLog().Index < args.PrevLogIndex {
		reply.Conflict = true // 日志不匹配，返回冲突信息
		reply.XTerm = -1
		reply.XIndex = -1
		reply.XLen = rf.log.len()
		DPrintf("[%v]: Conflict XTerm %v, XIndex %v, XLen %v", rf.me, reply.XTerm, reply.XIndex, reply.XLen)
		return
	}

	// 检查日志条目的任期是否匹配
	if rf.log.at(args.PrevLogIndex).Term != args.PrevLogTerm {
		reply.Conflict = true // 日志条目任期不匹配，返回冲突信息
		xTerm := rf.log.at(args.PrevLogIndex).Term
		// 计算出冲突的日志上一个任期的最后一条日志的索引
		for xIndex := args.PrevLogIndex; xIndex > 0; xIndex-- {
			if rf.log.at(xIndex-1).Term != xTerm {
				reply.XIndex = xIndex
				break
			}
		}
		reply.XTerm = xTerm
		reply.XLen = rf.log.len()
		DPrintf("[%v]: Conflict XTerm %v, XIndex %v, XLen %v", rf.me, reply.XTerm, reply.XIndex, reply.XLen)
		return
	}

	// 追加新条目到日志
	for idx, entry := range args.Entries {
		// 附加条目 RPC 规则 3：如果日志条目索引冲突，截断日志
		if entry.Index <= rf.log.lastLog().Index && rf.log.at(entry.Index).Term != entry.Term {
			rf.log.truncate(entry.Index)
			rf.persist()
		}
		// 附加条目 RPC 规则 4：如果日志条目索引大于当前最后一个日志条目索引，追加新条目
		if entry.Index > rf.log.lastLog().Index {
			rf.log.append(args.Entries[idx:]...)
			DPrintf("[%d]: follower append [%v]", rf.me, args.Entries[idx:])
			rf.persist()
			break
		}
	}

	// 附加条目 RPC 规则 5：更新提交索引并应用
	if args.LeaderCommit > rf.commitIndex {
		rf.commitIndex = min(args.LeaderCommit, rf.log.lastLog().Index)
		rf.apply()
	}
	reply.Success = true
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}
