　　本项目为 Mit6.824 分布式课程的 Lab2，也就是实现Raft算法，它被划分成了 Lab2A、Lab2B、Lab2C 三个实验：
<ul>
  <li style="list-style-type:none;">
    <ul>
      <li>Lab2A：leader 选举（leader election）、心跳（heartbeat）。</li>
      <li>Lab2B：日志复制（Log replication）。</li>
      <li>Lab2C：状态存储（state persistent）。</li>
    </ul>
  </li>
</ul>


## 前言

### 1、Raft 算法介绍
　　分布式共识算法是一种用于在分布式系统中达成一致性的方法。为了达到这个目标，Raft 主要做了两方面的事情：
<ul>
  <li style="list-style-type:none;">
    <ul>
      <li>问题分解：把共识算法分位 3 个子问题，分别是领导者选举、日志复制、安全性。</li>
      <li>状态简化：对算法做出一些限制，减少状态数量和可能产生的变动。</li>
    </ul>
  </li>
</ul>


### 2、Raft 节点
　　在任何时刻，每一个服务器节点都处于 leader、follower 或 candidate 这三个状态之一。   
　　正常运行的情况下，会有一个 leader，其他全为 follower，follower 只会响应 leader 和candidate 的请求，而客户端的请求则全部由 leader 处理，即使有客户端请求了一个 follower 也会将请求重定向到 leader。candidate 代表候选人，出现在选举 leader 阶段，选举成功后 candidate 将会成为新的 leader。


### 3、任期 term
　　在 Raft 协议中，将时间分成了一些任意长度的时间片，称为 term（也叫任期），term 使用连续递增的编号的进行识别，如下图所示：   

<div style="text-align: center;"> 
    <img src="./pictures/term.png" title="term">
</div> 

　　任期的机制可以非常明确地标识集群的状态。并且通过任期的比较，可以帮助我们确认一台服务器历史的状态。   
　　term 也起到了系统中逻辑时钟的作用，每一个 server 都存储了当前 term 编号，在 server 之间进行交流的时候就会带有该编号。

### 4、RPC 通信
　　server 之间的交流是通过 RPC 进行的。Raft 中有两种 RPC：
<ul>
  <li style="list-style-type:none;">
    <ul>
      <li>RequestVote RPC（请求投票）：它由 candidate 在选举期间发起，用于拉取选票</li>
      <li>AppendEntries RPC（追加条目）：它由 leader 发起，用于复制日志或者发送心跳信号</li>
    </ul>
  </li>
</ul>



## 一、leader 选举

### 1.1、什么时候开启选举？
　　心跳超时时。 Raft 内部有一种心跳机制，如果存在 leader，那么它就会周期性地向所有 follower 发送心跳，来维持自己的地位。
如果 follower 一段时间没有收到心跳，那么它就会认为系统中没有可用的 leader 了，然后开始进行选举。<br>
　　心跳超时存在以下几种情况：leader 故障、网络超时、Raft 集群启动时。

### 1.2、leader 选举流程
　　follower 先增加自己的当前任期号，并转换到 candidate 状态。然后投票给自己，并且并行地向集群中的其他服务器节点发送投票请求
（RequestVote RPC）。之后 candidate 状态将可能发生如下三种变化:<br>
　　① 它获得超过半数选票赢得了选举，成功 leader 并开始发送心跳。<br>
　　② 其他节点赢得了选举，收到新的 leader 的心跳后，如果新 leader 的任期号不小于自己当前的任期号，那么就从 candidate 回到
      follower 状态。<br>
　　③ 一段时间之后没有获胜者，每个 candidate 都在一个自己的随机选举超时时间后增加任期号开始新一轮投票。


## 二、日志复制
　　一条日志中需要具有三个信息：① 状态机指令；② leader 的任期号；③ 日志号（日志索引）；
### 2.1、日志一致性
　　如果不同日志中的两个日志项具有相同的索引值和任期编号，那么这两个日志项具有相同的指令。 <br>
　　如果不同日志中的两个日志项具有相同的索引值和任期编号，那么这两个日志项之前的所有日志项也全部相同。
### 2.2、日志复制的过程
　　两个步骤：① 日志复制；② 提交。<br>
　　生成日志后（心跳、写命令），leader 并行发送追加条目 RPC 给 follower（如果丢失会重发），让它们复制该条目。follower 收到日志后开始复制，
然后发送响应给 leader。leader 收到超过半数的复制成功消息后，开始提交，将日志更新至状态机并将结果返回客户端，而后再发送一个追加日志 RPC 触发 follower
的日志提交。最后 follower 收到提交请求时，开始进行提交。<br>
　　我们把本地执行指令，也就是 leader 应用日志到状态机这一步，称作提交（即执行该命令并且将执行结果返回客户端）。
<div style="text-align: center;"> 
    <img src="./pictures/log.png" title="term" width="600" height="235">
</div> 



## 三、实现

### 3.1、labrpc.Network
　　labrpc.Network 代表网络的抽象。<br>
**（1）ends <br>**
　　Network 保存了每个 Raft 节点接收数据的端点（socket 的抽象），即 ClientEnd。举个例子，节点 0 中保存了 3 个端点，
0->0, 0->1, 0->2。每个端点中存在一个通道，往通道中写入请求，相当于往对应的节点发送请求。另外端点使用随机的字符串作为 key 来标识，如 
wcOrWWIZMDAR-P22WBi3。<br>
**（2）enabled <br>**
　　Raf 集群中所有节点的启动状态，即机器是否开启。<br>
**（3）endCh <br>**
　　网络中传输的 RPC 请求。Netwrok 创建后会启动一个 goroutine 专门来从该通道获取请求，然后进行处理。


### 3.2、Raft 
　　代表 Raft 的一个节点，即状态机。

```go
type Raft struct {
	mu        sync.Mutex
	peers     []*labrpc.ClientEnd   // 其他Raft节点上用于接收数据的端点
	persister *Persister
	me        int
	dead      int32

	state         RaftState
	appendEntryCh chan *Entry
	heartBeat     time.Duration
	electionTime  time.Time

	currentTerm int
	votedFor    int
	log         Log    　 // 节点上的指令日志，严格按照顺序执行，则所有状态机都能达成一致

	commitIndex int
	lastApplied int

	nextIndex  []int
	matchIndex []int

	applyCh   chan ApplyMsg
	applyCond *sync.Cond
}
```

### checkOneLeader
　　检查集群中是否只存在一个 leader。此处循环 10 次的原因是：分布式系统中某时刻正在选举，可能没有 leader。
<div style="text-align: center;"> 
    <img src="./pictures/checkleaderone.jpg" title="term" width="400" height="400">
</div> 



### ticker
检查集群中是否只存在一个 leader。此处循环 10 次的原因是：分布式系统中某时刻正在选举，可能没有 leader。
<div style="text-align: center;"> 
    <img src="./pictures/ticker.jpeg" title="term" width="330" height="460">
</div> 




## 四、测试项
### 4.1、TestInitialElection2A
　　① 集群启动后，leader 选举是否成功。<br>
　　② 网络正常的情况下，2 个选举超时时间后，集群的 term 是否会改变，leader 是否正常。





```go
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	rf.state = Follower
	rf.currentTerm = 0
	rf.votedFor = -1
	rf.heartBeat = 100 * time.Millisecond
	rf.resetElectionTimer()

	rf.log = makeEmptyLog()
	rf.log.append(Entry{-1, 0, 0})
	rf.commitIndex = 0
	rf.lastApplied = 0
	rf.nextIndex = make([]int, len(rf.peers))
	rf.matchIndex = make([]int, len(rf.peers))

	rf.applyCh = applyCh
	rf.applyCond = sync.NewCond(&rf.mu)

	rf.readPersist(persister.ReadRaftState())

	go rf.ticker()

	go rf.applier()
	return rf
}
```

初始化Raft的时候，除了给raft做基本的赋值之外，还要额外启动两个goroutine。作业要求中提到不要使用Go内置的timer，在2021版的代码里新增了一个ticker函数，作用也很简单，计时并且按时间触发leader election或者append entry。而applier则是负责将command应用到state machine，这一点和论文中一致。

先看ticker()函数

## ticker

```go
func (rf *Raft) ticker() {
	for rf.killed() == false {
		time.Sleep(rf.heartBeat)
		rf.mu.Lock()
		if rf.state == Leader {
			rf.appendEntries(true)
		}
		if time.Now().After(rf.electionTime) {
			rf.leaderElection()
		}
		rf.mu.Unlock()
	}
}
```

ticker会以心跳为周期不断检查状态。如果当前是Leader就会发送心跳包，而心跳包是靠appendEntries()发送空log，而不是额外的函数，这一点在论文和student guide都有强调。

如果发现选举超时，这时候就会出发新一轮leader election。先看leader election的实现

## leader election
```go
func (rf *Raft) leaderElection() {
	rf.currentTerm++
	rf.state = Candidate
	rf.votedFor = rf.me
	rf.persist()
	rf.resetElectionTimer()
	term := rf.currentTerm
	voteCounter := 1
	lastLog := rf.log.lastLog()
	args := RequestVoteArgs{
		Term:         term,
		CandidateId:  rf.me,
		LastLogIndex: lastLog.Index,
		LastLogTerm:  lastLog.Term,
	}

	var becomeLeader sync.Once
	for serverId, _ := range rf.peers {
		if serverId != rf.me {
			go rf.candidateRequestVote(serverId, &args, &voteCounter, &becomeLeader)
		}
	}
}
```

启动新一轮leader election时，首先要将自己转为candidate状态，并且给自己投一票。然后向所有peer请求投票。RequestVote RPC的参数和返回值需要按照Figure 2实现。

```go
func (rf *Raft) candidateRequestVote(serverId int, args *RequestVoteArgs,    voteCounter *int, becomeLeader *sync.Once) {
	reply := RequestVoteReply{}
	ok := rf.sendRequestVote(serverId, args, &reply)
	if !ok {
		return
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if reply.Term > args.Term {
		rf.setNewTerm(reply.Term)
		return
	}
	if reply.Term < args.Term {
		return
	}
	if !reply.VoteGranted {
		return
	}

	*voteCounter++
	if *voteCounter > len(rf.peers)/2 &&
		rf.currentTerm == args.Term &&
		rf.state == Candidate {
		becomeLeader.Do(func() {
			rf.state = Leader
			lastLogIndex := rf.log.lastLog().Index
			for i, _ := range rf.peers {
				rf.nextIndex[i] = lastLogIndex + 1
				rf.matchIndex[i] = 0
			}
			rf.appendEntries(true)
		})
	}
}
```
除了要满足论文的Figure 2中*Rules for Servers*的要求之外，要注意当candidate收到半数以上投票之后就可以进入leader状态，而这个状态转变会更新nextIndex[]和matchIndex[]，并且再成为leader之后要立刻发送一次心跳。我们希望状态转变只发生一次，这里我使用了go的sync.Once。简单的使用bool flag也同样可以达成目的，只不过可读性没有这么直观。

## RequestVote

另一方面，任何服务器收到RequestVote RPC之后，要实现Figure 2中*RequestVote RPC Receiver implementation*的逻辑，同时也要满足*Rules for Servers*

```go
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// rules for servers
	// all servers 2
	if args.Term > rf.currentTerm {
		rf.setNewTerm(args.Term)
	}

	// request vote rpc receiver 1
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.VoteGranted = false
		return
	}

	// request vote rpc receiver 2
	myLastLog := rf.log.lastLog()
	upToDate := args.LastLogTerm > myLastLog.Term ||
		(args.LastLogTerm == myLastLog.Term && args.LastLogIndex >= myLastLog.Index)
	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && upToDate {
		reply.VoteGranted = true
		rf.votedFor = args.CandidateId
		rf.persist()
		rf.resetElectionTimer()
	} else {
		reply.VoteGranted = false
	}
	reply.Term = rf.currentTerm
}
```

论文5.2 & 5.4节详细解释了这部分逻辑的来源。

## AppendEntry

完成了leader election之后，leader会立刻触发一次心跳包，随后在每个心跳周期发送心跳包，来阻止新一轮leader election。
Figure 2中*Rules for Servers*的*Leaders*部分将心跳称为`initial empty AppendEntries RPCs (heartbeat)`，将包含log的RPC称为`AppendEntries RPC with log entries starting at nextIndex`。这种描述听起来像是用了两段不同的代码。
而实际上因为这里的心跳有两种理解：每个心跳周期，发送一次AppendEntries RPC，当这个RPC不包含log时，这个包被称为心跳包。所以也有可能发生这么一种情况：触发了一次心跳，但是带有log（即心跳周期到了，触发了一次AppendEntries RPC，但是由于follower落后了，所以这个RPC带有一段log，此时这个包就不能称为心跳包）。

实践中，我在每个心跳周期和收到新的command之后各会触发一次AppendEntries RPC。然而仔细读论文后发现，论文中并没有只说了心跳会触发AppendEntries RPC，并没有说收到客户端的指令之后应该触发AppendEntries RPC。

我甚至认为在理论上AppendEntries可以完全交给heartbeat周期来触发，即收到command后，并不立刻发送AppendEntries，而是等待下一个心跳。这种方法可以减少RPC的数量，并且通过了连续1000次测试。但是代价就是每条command的提交周期变长。

```go
func (rf *Raft) appendEntries(heartbeat bool) {
	lastLog := rf.log.lastLog()
	for peer, _ := range rf.peers {
		if peer == rf.me {
			rf.resetElectionTimer()
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
				Term:         rf.currentTerm,
				LeaderId:     rf.me,
				PrevLogIndex: prevLog.Index,
				PrevLogTerm:  prevLog.Term,
				Entries:      make([]Entry, lastLog.Index-nextIndex+1),
				LeaderCommit: rf.commitIndex,
			}
			copy(args.Entries, rf.log.slice(nextIndex))
			go rf.leaderSendEntries(peer, &args)
		}
	}
}
```
Leader在AppendEntries中会并行地给所有server发送消息，然后根据返回的消息更新nextIndex和matchIndex，这部分需要按照论文5.3节来实现。
但是同样在5.3节，作者提到了fast rollback优化。Morris的讲座上，实现这种优化需要在返回消息中额外加入XTerm, XIndex, XLen三个字段。
```go
type AppendEntriesReply struct {
	Term     int
	Success  bool
	Conflict bool
	XTerm    int
	XIndex   int
	XLen     int
}
```
原作的说法上，这种优化可能不是必须的，所以并不作为raft核心算法的一部分。实际上，我感觉如果直接在raft-core的代码上实现，有可能会引入一个小bug，不影响运行但可能会拖效率。然而这点我也不好证明，只能说里面多半有一部分冗余代码，但是我也不敢删，所以就留着……

```go
func (rf *Raft) leaderSendEntries(serverId int, args *AppendEntriesArgs) {
	var reply AppendEntriesReply
	ok := rf.sendAppendEntries(serverId, args, &reply)
	if !ok {
		return
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if reply.Term > rf.currentTerm {
		rf.setNewTerm(reply.Term)
		return
	}
	if args.Term == rf.currentTerm {
		// rules for leader 3.1
		if reply.Success {
			match := args.PrevLogIndex + len(args.Entries)
			next := match + 1
			rf.nextIndex[serverId] = max(rf.nextIndex[serverId], next)
			rf.matchIndex[serverId] = max(rf.matchIndex[serverId], match)
		} else if reply.Conflict {
			if reply.XTerm == -1 {
				rf.nextIndex[serverId] = reply.XLen
			} else {
				lastLogInXTerm := rf.findLastLogInTerm(reply.XTerm)
				if lastLogInXTerm > 0 {
					rf.nextIndex[serverId] = lastLogInXTerm
				} else {
					rf.nextIndex[serverId] = reply.XIndex
				}
			}
		} else if rf.nextIndex[serverId] > 1 {
			rf.nextIndex[serverId]--
		}
		rf.leaderCommitRule()
	}
}
```
当peer收到AppendEntry RPC的时候，需要实现Figure 2中*AppendEntry RPC Receiver implementation* + *Rules for Servers*。具体哪些相关，我已经加在注释里了。论文里的步骤必须严格遵守，不要自由发挥。这一点想必大家在debug的时候都深有体会……

```go
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	// rules for servers
	// all servers 2
	reply.Success = false
	reply.Term = rf.currentTerm
	if args.Term > rf.currentTerm {
		rf.setNewTerm(args.Term)
		return
	}

	// append entries rpc 1
	if args.Term < rf.currentTerm {
		return
	}
	rf.resetElectionTimer()

	// candidate rule 3
	if rf.state == Candidate {
		rf.state = Follower
	}
	// append entries rpc 2
	if rf.log.lastLog().Index < args.PrevLogIndex {
		reply.Conflict = true
		reply.XTerm = -1
		reply.XIndex = -1
		reply.XLen = rf.log.len()
		return
	}
	if rf.log.at(args.PrevLogIndex).Term != args.PrevLogTerm {
		reply.Conflict = true
		xTerm := rf.log.at(args.PrevLogIndex).Term
		for xIndex := args.PrevLogIndex; xIndex > 0; xIndex-- {
			if rf.log.at(xIndex-1).Term != xTerm {
				reply.XIndex = xIndex
				break
			}
		}
		reply.XTerm = xTerm
		reply.XLen = rf.log.len()
		return
	}

	for idx, entry := range args.Entries {
		// append entries rpc 3
		if entry.Index <= rf.log.lastLog().Index && rf.log.at(entry.Index).Term != entry.Term {
			rf.log.truncate(entry.Index)
			rf.persist()
		}
		// append entries rpc 4
		if entry.Index > rf.log.lastLog().Index {
			rf.log.append(args.Entries[idx:]...)
			rf.persist()
			break
		}
	}

	// append entries rpc 5
	if args.LeaderCommit > rf.commitIndex {
		rf.commitIndex = min(args.LeaderCommit, rf.log.lastLog().Index)
		rf.apply()
	}
	reply.Success = true
}
```
完成AppendEntry RPC之后，Leader需要提交已有的日志条目，这一点在论文5.3 & 5.4有文字叙述。但是具体在什么时候提交，需要自己去把握。仔细看Figure 2的话，其实这部分对应*Rules for Servers*中Leader部分的最后一小段

```go
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
				rf.apply()
				break
			}
		}
	}
}
```

## applier

student guide中提到应该使用一个`a dedicated “applier”`来专门处理日志commit的事情。所以按TA说的来，并且按照作业要求使用applyCond。这里可能会触发student guide所说的`The four-way deadlock`，不过guide中也给出了解决方案。不重复赘述，文末有中文版的链接，自己去读。

```go
func (rf *Raft) apply() {
	rf.applyCond.Broadcast()
}

func (rf *Raft) applier() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	for !rf.killed() {
		if rf.commitIndex > rf.lastApplied && rf.log.lastLog().Index > rf.lastApplied {
			rf.lastApplied++
			applyMsg := ApplyMsg{
				CommandValid: true,
				Command:      rf.log.at(rf.lastApplied).Command,
				CommandIndex: rf.lastApplied,
			}
			rf.mu.Unlock()
			rf.applyCh <- applyMsg
			rf.mu.Lock()
		} else {
			rf.applyCond.Wait()
		}
	}
}
```

## Start

最后是start函数，它会接受客户端的command，并且应用raft算法。前面也说了，每次start并不一定要立刻触发AppendEntry。理论上如果每次都触发AppendEntry，而start被调用的频率又超高，Leader就会疯狂发送RPC。如果不主动触发，而被动的依赖心跳周期，反而可以造成batch operation的效果，将QPS固定成一个相对较小的值。当中的trade-off需要根据使用场景自己衡量。

```go
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != Leader {
		return -1, rf.currentTerm, false
	}
	index := rf.log.lastLog().Index + 1
	term := rf.currentTerm

	log := Entry{
		Command: command,
		Index:   index,
		Term:    term,
	}
	rf.log.append(log)
	rf.persist()
	rf.appendEntries(false)

	return index, term, true
}
```

## 总结

1. 一定要按照论文+student guide来实现，完全按照论文确实可以完美复现。但是话说回来，都做到这份上了，为啥不直接给个伪代码版本。。。
2. 千万不要过早优化。直接使用函数粒度的锁，细粒度的锁在提升性能的同时，会增加复杂度，尤其debug的难度，并且这个难度在复杂的高并发+不可靠的网络背景下可以无限上升。等待debug难度过大，就只能删掉重构了。
3. 通过单次测试只是第一步，真正的考验才刚刚开始。很多bug出现的概率不高（话说统计课上将概率低于5%叫做小概率事件，然而这种bug到处都是……
4. 所以debug的log一定要写详细点，向我单跑一次TestFigure8Unreliable2C能打出两万条log
5. 接上条，早点写个log可视化的脚本来处理。Python写了一下，大约30多行，可以把45s左右的test过程，变成一个5分钟左右的动画，能看到每个server的append、commit等过程
6. 论文+student guide需要反复看，所以早点把重点摘出来写成笔记放在手边。我在微信上发了中文版的翻译



## 参考链接：

- https://github.com/s09g/raft-go/tree/main
- https://www.bilibili.com/video/BV1pr4y1b7H5/?spm_id_from=333.337.search-card.all.click&vd_source=ff9932351ef409bb94e9586a7891b82e








