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
			rf.resetElectionTimer() // 重置 electionTime，并跳过
			continue
		}

		// 发送快照
		if rf.nextIndex[peer]-1 < rf.lastIncludeIndex {
			go rf.leaderSendSnapShot(peer)
			return
		}

		// rules for leader 3
		if lastLog.Index >= rf.nextIndex[peer] || heartbeat {
			nextIndex := rf.nextIndex[peer]
			if nextIndex <= 0 {
				nextIndex = 1
			}
			if lastLog.Index < nextIndex {
				nextIndex = lastLog.Index
			}
			prevLogIndex, prevLogTerm := rf.getPrevLogInfo(peer)
			args := AppendEntriesArgs{
				Term:         rf.currentTerm, // 任期
				LeaderId:     rf.me,          // leaderID，集群节点数据的下标
				PrevLogIndex: prevLogIndex,   // 上一个日志的index
				PrevLogTerm:  prevLogTerm,    // 上一个日志的任期
				LeaderCommit: rf.commitIndex,
			}
			// 填充日志
			if rf.getLastIndex() >= rf.nextIndex[peer] {
				entries := make([]Entry, 0)
				entries = append(entries, rf.log.Entries[nextIndex-rf.lastIncludeIndex:]...)
				args.Entries = entries
			} else {
				args.Entries = []Entry{}
			}
			go rf.leaderSendEntries(peer, &args) // 给节点发送追加条目RPC
		}
	}
}

// leaderSendEntries 给 serverId 发送追加条目RPC（args，日志复制或者发送心跳）
func (rf *Raft) leaderSendEntries(serverId int, args *AppendEntriesArgs) {
	var reply AppendEntriesReply
	ok := rf.sendAppendEntries(serverId, args, &reply)
	if !ok {
		return
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 如果响应的任期大于leader节点的任期，说明
	if reply.Term > rf.currentTerm {
		rf.setNewTerm(reply.Term)
		return
	}

	// 防止在复制日志的时候崩溃的情况
	if args.Term == rf.currentTerm {
		if reply.Success { // 如果 follower 日志复制成功
			match := args.PrevLogIndex + len(args.Entries)
			next := match + 1
			rf.nextIndex[serverId] = max(rf.nextIndex[serverId], next)    // 更新要发送给该服务器的下一个日志条目的索引，为什么要使用 max 呢？
			rf.matchIndex[serverId] = max(rf.matchIndex[serverId], match) // 更新已复制到该服务器的最高日志条目的索引
			DPrintf("[%v]: %v append success next %v match %v", rf.me, serverId, rf.nextIndex[serverId], rf.matchIndex[serverId])
		} else if reply.Conflict {
			DPrintf("[%v]: Conflict from %v %#v", rf.me, serverId, reply)
			if reply.XTerm == -1 { //prelog 缺失
				rf.nextIndex[serverId] = reply.XLen // 设置 nextIndex 为 follower 的日志长度
			} else { //prelog 没有缺失，但是 Term 不同
				lastLogInXTerm := rf.findLastLogInTerm(reply.XTerm) //　在当前节点中寻找 prelog　的 term 最后一条日志
				DPrintf("[%v]: lastLogInXTerm %v", rf.me, lastLogInXTerm)
				if lastLogInXTerm > 0 { // 如果找到，则设置 nextIndex
					rf.nextIndex[serverId] = lastLogInXTerm
				} else {
					rf.nextIndex[serverId] = reply.XIndex // 如果没找到，则设置 nextIndex 为冲突日志的上一个 term 的最后一条日志。为什么不是 reply.XIndex + 1 呢
				}
			}

			DPrintf("[%v]: leader nextIndex[%v] %v", rf.me, serverId, rf.nextIndex[serverId])
		} else if rf.nextIndex[serverId] > 1 {
			rf.nextIndex[serverId]-- // 将 nextIndex 回退，以便重试，Raft 算法的一致性规则
			//if reply.XSnapFlag {
			//	rf.nextIndex[serverId] = reply.XIndex      // 将 nextIndex 回退，以便重试，Raft 算法的一致性规则
			//	rf.matchIndex[serverId] = reply.XIndex - 1 // 更新已复制到该服务器的最高日志条目的索引
			//}
		}
		rf.leaderCommitRule()
	}
}

// 返回节点任期为 x 的最新的一个日志 index
func (rf *Raft) findLastLogInTerm(x int) int {
	for i := rf.getLastIndex(); i > rf.lastIncludeIndex; i-- {
		term := rf.restoreLogTerm(i)
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

	for n := rf.commitIndex + 1; n <= rf.getLastIndex(); n++ {
		//if rf.restoreLogTerm(n) != rf.currentTerm { // 不能提交之前任期内的日志条目
		//	continue
		//}
		counter := 1
		for serverId := 0; serverId < len(rf.peers); serverId++ {
			if serverId != rf.me && rf.matchIndex[serverId] >= n { // 遍历每个 follower，如果已经复制了该日志
				counter++
			}
			if counter > len(rf.peers)/2 { //如果复制的个数达到一半
				rf.commitIndex = n
				DPrintf("[%v] leader 尝试提交 index %v", rf.me, rf.commitIndex)
				rf.apply() // 表示命令可以执行了
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

	// follower 收到在自己快照之前的日志，返回 true
	if rf.lastIncludeIndex > args.PrevLogIndex { // true 代表所有的日志都复制成功
		reply.Success = false
		reply.XIndex = rf.lastIncludeIndex + 1
		return
	}

	// 检查日志是否与前一个日志条目匹配
	if rf.getLastIndex() < args.PrevLogIndex {
		reply.Conflict = true // 日志不匹配，返回冲突信息
		reply.XTerm = -1
		reply.XIndex = -1
		reply.XLen = rf.getLastIndex()
		DPrintf("[%v]: Conflict XTerm %v, XIndex %v, XLen %v", rf.me, reply.XTerm, reply.XIndex, reply.XLen)
		return
	}

	// 检查日志条目的任期是否匹配
	if rf.restoreLogTerm(args.PrevLogIndex) != args.PrevLogTerm {
		reply.Conflict = true // 日志条目任期不匹配，返回冲突信息
		xTerm := rf.restoreLogTerm(args.PrevLogIndex)
		// 找到冲突 term 的首次出现位置
		for xIndex := args.PrevLogIndex; xIndex > rf.lastIncludeIndex; xIndex-- {
			if rf.restoreLogTerm(xIndex-1) != xTerm {
				reply.XIndex = xIndex // xIndex-1
				break
			}
		}
		reply.XTerm = xTerm
		reply.XLen = rf.getLastIndex()
		DPrintf("[%v]: Conflict XTerm %v, XIndex %v, XLen %v", rf.me, reply.XTerm, reply.XIndex, reply.XLen)
		return
	}

	//// 追加新条目到日志
	//for idx, entry := range args.Entries {
	//	// 附加条目 RPC 规则 3：如果日志条目索引冲突，截断日志
	//	if entry.Index <= rf.log.lastLog().Index && rf.log.at(entry.Index).Term != entry.Term {
	//		rf.log.truncate(entry.Index)
	//		rf.persist()
	//	}
	//	// 附加条目 RPC 规则 4：如果日志条目索引大于当前最后一个日志条目索引，追加新条目
	//	if entry.Index > rf.log.lastLog().Index {
	//		rf.log.append(args.Entries[idx:]...)
	//		DPrintf("[%d]: follower append [%v]", rf.me, args.Entries[idx:])
	//		rf.persist()
	//		break
	//	}
	//}

	rf.log.Entries = append(rf.log.Entries[:args.PrevLogIndex+1-rf.lastIncludeIndex], args.Entries...)

	rf.persist()

	// 附加条目 RPC 规则 5：更新提交索引并应用
	if args.LeaderCommit > rf.commitIndex {
		rf.commitIndex = min(args.LeaderCommit, rf.getLastIndex()) // 更新本节点的已提交位置
		rf.apply()
	}
	reply.Success = true
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}
