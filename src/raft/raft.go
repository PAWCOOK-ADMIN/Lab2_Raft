package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"6.824/labgob"
	"6.824/labrpc"
)

// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part 2D you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.
type ApplyMsg struct {
	CommandValid bool        // 表示该 ApplyMsg 是否包含一个新提交的日志条目。
	Command      interface{} // 需要应用的命令，可以是任意类型。
	CommandIndex int         // 该命令在日志中的索引。

	// 下面字段用于 2D 部分：
	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

type RaftState string

const (
	Follower  RaftState = "Follower"
	Candidate           = "Candidate"
	Leader              = "Leader"
)

// 代表raft的一个节点，即一个状态机
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // 其他Raft节点上用于接收数据的端点。即（0，1）、（0，2）、（0，3）
	persister *Persister          // 用于保存节点持久化信息的对象
	me        int                 // 当前Raft节点的下标
	dead      int32               // 由 Kill() 函数设置，崩溃则值为1

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.

	state         RaftState // 节点的状态，leader、Follower、Candidate
	appendEntryCh chan *Entry
	heartBeat     time.Duration // 心跳间隔
	electionTime  time.Time     // 应该进行leader选举的时间

	// Persistent state on all servers:
	currentTerm int
	votedFor    int
	log         Log // 节点上的指令日志，严格按照顺序执行，则所有状态机都能达成一致

	// Volatile state on all servers:
	commitIndex int
	lastApplied int

	// Volatile state on leaders:
	nextIndex  []int //
	matchIndex []int

	applyCh   chan ApplyMsg
	applyCond *sync.Cond // 条件变量
}

// 将 Raft 的持久状态保存到稳定存储中，以便在崩溃和重启后可以检索到。
// 参见论文的图 2 以了解应该持久化的内容。
func (rf *Raft) persist() {
	DPrintVerbose("[%v]: STATE: %v", rf.me, rf.log.String()) // 打印当前节点的状态信息，主要用于调试和日志记录
	w := new(bytes.Buffer)                                   // 创建一个字节缓冲区，用于临时存储编码后的数据
	e := labgob.NewEncoder(w)                                // 创建一个新的编码器，将数据写入字节缓冲区

	e.Encode(rf.currentTerm) // 将 currentTerm 编码并写入缓冲区
	e.Encode(rf.votedFor)
	e.Encode(rf.log)

	data := w.Bytes()                // 获取缓冲区中的字节数组，即编码后的数据
	rf.persister.SaveRaftState(data) // 将编码后的数据保存到稳定存储中，以便在崩溃和重启后可以恢复
}

// restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}

	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var currentTerm int
	var votedFor int
	var logs Log

	if d.Decode(&currentTerm) != nil || d.Decode(&votedFor) != nil || d.Decode(&logs) != nil {
		log.Fatal("failed to read persist\n")
	} else {
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.log = logs
	}
}

// A service wants to switch to snapshot.  Only do so if Raft hasn't
// have more recent info since it communicate the snapshot on applyCh.
func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {

	// Your code here (2D).

	return true
}

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).

}

// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
// 接受客户端的command，并且应用在raft的算法中
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != Leader { // 如果不是leader，则结束
		return -1, rf.currentTerm, false
	}
	index := rf.log.lastLog().Index + 1 // 指令日志索引号+1
	term := rf.currentTerm

	// 新增一条日志保存到状态机中
	log := Entry{
		Command: command,
		Index:   index,
		Term:    term,
	}
	rf.log.append(log)

	// 持久化
	rf.persist()
	DPrintf("[%v]: term %v Start %v", rf.me, term, log)

	// 发送追加条目RPC
	rf.appendEntries(false)

	return index, term, true
}

// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

// ticker 以心跳为周期不断检查自己的状态，如果没有收到心跳包，则开始一个新的leader选举过程
func (rf *Raft) ticker() {
	// 如果节点没有崩溃，则一直执行以下循环
	for rf.killed() == false {

		// 睡眠一个心跳的时间
		time.Sleep(rf.heartBeat)
		rf.mu.Lock()

		// 如果是leader，醒来后发送一个心跳包（追加条目RPC）
		if rf.state == Leader {
			rf.appendEntries(true)
		}

		// 如果醒来后，发现此刻在应该进行选举的超时时间之后，则说明没有收到心跳包，开启新的一轮leader选举
		if time.Now().After(rf.electionTime) {
			rf.leaderElection()
		}
		rf.mu.Unlock()
	}
}

// Make 服务或测试者希望创建一个 Raft 服务器。
// 所有 Raft 服务器的端口（包括这个服务器的端口）都在 peers[] 数组中。
// 这个服务器的端口是 peers[me]。
// 所有服务器的 peers[] 数组的顺序都是相同的。
// persister 是一个用于保存这个服务器的持久状态的地方，并且最初还包含最近保存的状态（如果有的话）。
// applyCh 是一个通道，测试者或服务期望 Raft 在这个通道上发送 ApplyMsg 消息。
// Make 函数必须快速返回，因此应该为任何长时间运行的工作启动 goroutines。
// 创建一个 Raft 集群的节点
func Make(peers []*labrpc.ClientEnd, me int, persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (2A, 2B, 2C).
	rf.state = Follower
	rf.currentTerm = 0 // 初始化任期为 0
	rf.votedFor = -1
	rf.heartBeat = 50 * time.Millisecond // 50毫秒
	rf.resetElectionTimer()

	rf.log = makeEmptyLog()        // 初始化保存日志的结构
	rf.log.append(Entry{-1, 0, 0}) // 增加一条空日志
	rf.commitIndex = 0
	rf.lastApplied = 0
	rf.nextIndex = make([]int, len(rf.peers))
	rf.matchIndex = make([]int, len(rf.peers))

	rf.applyCh = applyCh
	rf.applyCond = sync.NewCond(&rf.mu)

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// 启动一个定时器，开始leader选举
	go rf.ticker()

	go rf.applier()
	return rf
}

func (rf *Raft) apply() {
	rf.applyCond.Broadcast()
	DPrintf("[%v]: rf.applyCond.Broadcast()", rf.me)
}

// 日志提交
func (rf *Raft) applier() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 如果节点没有崩溃，则一直执行以下循环
	for !rf.killed() {
		// all server rule 1
		if rf.commitIndex > rf.lastApplied && rf.log.lastLog().Index > rf.lastApplied {
			rf.lastApplied++
			applyMsg := ApplyMsg{
				CommandValid: true,
				Command:      rf.log.at(rf.lastApplied).Command,
				CommandIndex: rf.lastApplied,
			}
			DPrintVerbose("[%v]: COMMIT %d: %v", rf.me, rf.lastApplied, rf.commits())
			rf.mu.Unlock()
			rf.applyCh <- applyMsg
			rf.mu.Lock()
		} else {
			rf.applyCond.Wait()
			DPrintf("[%v]: rf.applyCond.Wait()", rf.me)
		}
	}
}

func (rf *Raft) commits() string {
	nums := []string{}
	for i := 0; i <= rf.lastApplied; i++ {
		nums = append(nums, fmt.Sprintf("%4d", rf.log.at(i).Command))
	}
	return fmt.Sprint(strings.Join(nums, "|"))
}
