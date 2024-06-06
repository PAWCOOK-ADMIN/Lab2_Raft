　　本项目为 Mit6.824 分布式课程的 Lab2，也就是实现 Raft 算法，它被划分成了 Lab2A、Lab2B、Lab2C、Lab2D 四个实验。<br>
**（1）Lab2A<br>**
　　leader 选举（leader election）、心跳（heartbeat）。<br>
**（2）Lab2B<br>**
　　日志复制（Log replication）。<br>
**（3）Lab2C<br>**
　　状态存储（state persistent）。当 currentTerm、voteFor、log[] 更新后，调用 persister 将它们持久化下来，因为这 3 个状态是要求持久化的。<br>
　　优化 nextIndex 的回退性能。即 appendEntries 被拒时，论文默认反复减 1 回退重试，导致耗费很长时间才能找到同步位置，优化后可以一次性跳过更多的 index，减少 RPC 往复。<br>
**（4）Lab2D<br>**
　　实现日志压缩。Raft 是基于日志复制的共识算法，对于每执行一条命令，就需要新增日志，一旦日志过长，便会拖垮服务器，因此必须实现日志压缩。


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
　　在任何时刻，每一个服务器节点都处于 leader、follower 或 candidate 这三个状态之一。 <br>
　　正常运行的情况下，会有一个 leader，其他全为 follower，follower 只会响应 leader 和candidate 的请求，而客户端的请求则全部由 leader 处理，即使有客户端请求了一个 follower 也会将请求重定向到 leader。candidate 代表候选人，出现在选举 leader 阶段，选举成功后 candidate 将会成为新的 leader。 <br>
　　在 Raft 中，每个节点都维护了一个日志条目的有序序列，这些日志条目记录了系统中的操作。节点之间通过相互通信来进行日志的复制和同步，以达到一致的状态。

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

### 1.1、为什么要选举出 leader？
　　可以简化设计。<br>
　　顺序一致性：通过由 leader 负责分发日志条目，确保所有节点以相同的顺序记录和应用日志。<br>
　　简化协调：leader 可以协调所有对日志条目的写入和读取操作，确保系统状态的一致性，简化了冲突处理和协调的复杂性。<br>
　　单一写入源：leader 作为单一的写入源，保证了日志条目的一致写入，避免了分布式系统中可能出现的写入冲突和不一致问题。<br>

### 1.2、什么时候开启选举？
　　心跳超时时。 Raft 内部有一种心跳机制，如果存在 leader，那么它就会周期性地向所有 follower 发送心跳，来维持自己的地位。
如果 follower 一段时间没有收到心跳，那么它就会认为系统中没有可用的 leader 了，然后开始进行选举。<br>
　　心跳超时存在以下几种情况：leader 故障、网络超时、Raft 集群启动时。

### 1.3、leader 选举流程
　　follower 先增加自己的当前任期号，并转换到 candidate 状态。然后投票给自己，并且并行地向集群中的其他服务器节点发送投票请求
（RequestVote RPC）。之后 candidate 状态将可能发生如下三种变化:<br>
　　① 它获得超过半数选票赢得了选举，成功 leader 并开始发送心跳。<br>
　　② 其他节点赢得了选举，收到新的 leader 的心跳后，如果新 leader 的任期号不小于自己当前的任期号，那么就从 candidate 回到
      follower 状态。<br>
　　③ 一段时间之后没有获胜者，每个 candidate 都在一个自己的随机选举超时时间后增加任期号开始新一轮投票。

### 1.4、如何确保新的 leader 拥有集群中所有已提交日志？
　　方法：只给日志比自己新的机器投票。问题又来了，为什么这样可以保证选举出来的leader拥有所有的提交日志呢？<br>

**（1）以下图中的 a 举例 <br>**
　　假设 s1，s2 都崩溃了，则 leader 会从 3，4，5 中产生，包含了所有提交的日志。<br>
　　假设 s1 崩溃了，则 leader 会从 2，3，4，5 中产生，包含了所有提交的日志。 <br>

**（2）还是下图中的 a，不过假设这时 s3 也复制了日志 (2,2)，并且集群提交了日志 2(2,2)<br>**
　　如果 s1，s2 都崩溃了，则 leader 一定会是 s3，因为它不会投票给 s4 和 s5，包含了所有提交的日志。

<div style="text-align: center;"> 
    <img src="./pictures/example3.png" title="term" width="350" height="200">
</div> 

　　也就是说，由于票数过半才能当选为 leader，并且只给日志比自己新的节点投票。所以 leader 只会在包含全部已提交日志的机器上产生。如果没提交，则没有影响。

注：Raft 通过比较两份日志中最后一条日志条目的索引值和任期号来定义谁的日志比较新。<br>
　　① 如果两份日志最后条目的任期号不同，那么任期号大的日志更 “新”。<br>
　　② 如果两份日志最后条目的任期号相同，那么日志较长的那个更“新”。


## 二、日志复制
　　一条日志中需要具有三个信息：① 状态机指令；② leader 的任期号；③ 日志号（日志索引）；
### 2.1、日志一致性
- **性质 1：如果两个不同日志中的条目具有相同的索引和任期，那么它们存储相同的命令**。原因：相同任期和索引的日志只有一条，而且日志是按序复制的，故相同任期下，索引为 2 的日志是不会存在与索引为 1 的位置中。


- **性质 2：如果两个不同日志中的条目具有相同的索引和任期，那么这些日志在所有先前的条目中都是相同的**。原因：Raft 的一致性协议保证的。
### 2.2、日志复制的过程
　　两个步骤：① 日志复制；② 提交。<br>
　　生成日志后（心跳、写命令），leader 并行发送追加条目 RPC 给 follower（如果丢失会重发），让它们复制该条目。follower 收到日志后开始复制，
然后发送响应给 leader。leader 收到超过半数的复制成功消息后，开始提交，将日志更新至状态机并将结果返回客户端，而后再发送一个追加日志 RPC 触发 follower
的日志提交。最后 follower 收到提交请求时，开始进行提交。<br>
　　我们把本地执行指令，也就是 leader 应用日志到状态机这一步，称作提交（即执行该命令并且将执行结果返回客户端）。
<div style="text-align: center;"> 
    <img src="./pictures/log.png" title="term" width="600" height="235">
</div> 

### 2.3、leader 什么时候会发送追加日志 RPC 来进行日志复制？

1、leader 收到客户端的命令时。<br>
2、ader 收到客户端的命令时。<br>


## 三、日志压缩
　　Raft 采用的是一种快照技术，每个节点在达到一定条件之后，可以把当前日志中的命令都写入自己的快照，然后就可以把已经并入快照的日志都删除了。

　　如下图所示：x 的更新信息依次是 3、2、0、5。y 的更新信息依次是 1、9、7，且日志下标 1~5 的日志被 commit 了，说明这段日志对当前节点来说已经不再需要。那么我们就存取这段日志的最后存储信息当做日志，也就是 x=0，y=9，同时记录下最后的快照存储的日志下标以及其对应的任期。此时我们新的日志就只需要 6、7 未提交的部分，log 的长度也从 7 变为了 2。
<div style="text-align: center;"> 
    <img src="./pictures/example10.png" title="term" width="330" height="210">
</div> 

　　last included index：快照替换的最后一个日志的索引，即图中第 5 个日志。<br>
　　last included term：快照替换的最后一个日志的任期，即图中第 5 个日志。<br>
　　之所以快照中需要上面内容，原因是需要支持快照后第一个日志的一致性检查。因为复制该日志时需要前一个日志索引和任期。<br>

　　一旦节点完成写入快照，它可以删除所有到 last included index 为止的日志条目，以及任何先前的快照。

　　虽然服务器通常会独立地拍摄快照，但领导者偶尔必须向落后的追随者发送快照，比如老的日志被清理了，这时如何该向一个异常缓慢的 follower 或一个新加入集群的节点复制日志呢？Raft 的策略是直接向 Follower 发送自己的快照。<br>

　　发送快照的是一个新类型的 RPC（InstallSnapshot），如下图所示：
<div style="text-align: center;"> 
    <img src="./pictures/example11.png" title="term" width="450" height="600">
</div> 

　　写入快照可能需要相当长的时间。因此 Raft 的实现是使用写时复制技术，即 Linux 上的 fork。

　　**问题：为什么要进行日志压缩呢？<br>**
　　因为随着 Raft 集群的不断运行，各状态机上的 log 也在不断积累，总会有一个时间会把状态机的内存打爆，所有我们需要有一个机制来安全地清理状态机上的 log。<br>

　　**问题：为什么 follower 自己独立地拍摄快照，而不是 leader 拍摄快照后发送给 follower 呢？<br>**
　　因为后一种有两个缺点。首先，将快照发送给每个 follower 会浪费网络带宽并减慢快照的完成。每个 follower 都已经拥有了生成自己的快照所需的信息，所以从其本地状态生成快照通常比通过网络发送和接收快照要快速得多。其次，leader 的执行将更加复杂。例如，leader 需要向 follower 发送快照，同时向他们复制新的日志条目，以便不阻止新的客户机请求。<br>

　　**问题：何时进行快照？<br>** 
　　如果服务器过于频繁地进行快照，会浪费磁盘带宽和能源；如果快照太不频繁，则有耗尽存储容量的风险，并且在重启时会增加重放日志所需的时间。<br>
　　一种简单的策略是在日志达到固定大小（以字节为单位）时进行快照。<br>




## 四、安全性
### 4.1、新 leader 是否提交之前任期内的日志条目

<div style="text-align: center;"> 
    <img src="./pictures/example1.png" title="term" width="350" height="200">
</div> 

　　① 假设在 c 中，S1 当选 leader 后提交了之前任期内的日志 2，而后 S1 崩溃了。<br>
　　② 这时假设 S5 当选 leader。因为日志号和 S2，S3 相同，但任期比 S2，S3 长。同时因为日志号和任期号都比 S4 长，因此也可以收到 S2，S3，S4 的选票。<br>
　　③ S5 当选后，发生了 follower 中的日志和新 leader 的日志不相同的情况，这时 Raft 会强制 follower 复制 leader 的日志来解决这个情况，即 S1，S2，S3，S4 
会强制复制 S5 的日志，并把日志 2 进行覆盖。<br>

　　因此，为了防止上面的情况发生，Raft 规定：<span style="color: red;">**leader 不能提交之前任期内未提交的日志**</span>。<br>

**1、日志的 “幽灵复现”**<br>
　　leader 不能提交之前任期内未提交的日志会进一步引出日志的 “幽灵复现” 问题。如下图 d1， 当 S5 当选 leader 并把 index=2 & term =3 的日志复制到了其他节点后，S5 还是不能提交该日志的。
但如果一直没新的请求进来，该日志岂不是就一直不能提交？<br>

```go
　　一个简单的例子就是转账场景，如果第一次查询转账结果时，发现未生效而重试，而转账事务日志作为幽灵复现日志重新出现的话，就造成了用户重复转账。
这个生效的时间如果太久，是不能够接收的。
```
　　所以 Raft 论文提到了引入 no-op 日志来解决这个问题。具体做法是：一个节点当选 leader 后，立刻发送一条自己当前任期的日志。这样，就可以把之前任期内满足提交条件的日志都提交了。如下图的 d2 所示。


<div style="text-align: center;"> 
    <img src="./pictures/example2.png" title="term" width="550" height="400">
</div> 



## 五、实现

### 5.1、网络
　　labrpc.Network 代表网络的抽象。<br>
- **ends** <br>
　　Network 保存了每个 Raft 节点接收数据的端点（socket 的抽象），即 ClientEnd。举个例子，节点 0 中保存了 3 个端点，
0->0, 0->1, 0->2。每个端点中存在一个通道，往通道中写入请求，相当于往对应的节点发送请求。另外端点使用随机的字符串作为 key 来标识，如 
wcOrWWIZMDAR-P22WBi3。<br>
- **enabled** <br>
　　Raf 集群中所有节点的启动状态，即机器是否开启。<br>
- **endCh** <br>
　　网络中传输的 RPC 请求。Netwrok 创建后会启动一个 goroutine 专门来从该通道获取请求，然后进行处理。


### 5.2、Raft 
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
	heartBeat     time.Duration    // 心跳间隔
	electionTime  time.Time        // 选举超时时间，一个绝对时间。

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

#### 1、Raft 节点的启动
　　节点的启动会在多种情况下发生，比如崩溃后重启，Raft 集群启动等。所以在启动需要先 kill 掉之前现有的节点实例。实现过程则是：① node 实例的状态设置为离线；② Network 保存的出站入站相关端点的状态设置为关闭；③ 删除 node 对应的 RPC 代理；<br>
　　在新增 node 实例后，会新建两个 Goroutine，一个作为定时器，周期性的触发心跳或者开启一个新的 leader 选举周期。另一个用于监听日志提交的 RPC 请求，并进行提交。<br>
　　最后还会为每个 node 创建一个 RPC 代理，用来接收并处理来自其他节点的 RPC 请求。
<div style="text-align: center;"> 
    <img src="pictures/node_start.jpeg" title="term" width="780" height="600">
</div> 


#### 2、定时器（ticker）
　　每个 Raft 节点启动后，都会新建一个 Goroutine 来启动自己的定时器。如果是节点是 leader，则定时用来发送心跳包。否则，
判断是否应该开启新一轮的 leader 选举。
<div style="text-align: center;"> 
    <img src="./pictures/ticker.jpeg" title="term" width="320" height="470">
</div> 


### 5.3、leader 选举
　　启动新一轮 leader 选举时，首先要将自己转为 candidate 状态，并且给自己投一票。然后向所有其他节点请求投票。要注意当 candidate 收到半数以上投票之后就可以进入 leader 状态，而这个状态转变会更新 nextIndex[] 和  matchIndex[]，并且再成为 leader 之后要立刻发送一次心跳。我们希望状态转变只发生一次，因此这里使用了 go 的 sync.Once。<br>
　　leader 当选后立即给其他 follower 发送一个心跳包，其目的主要有 2 个：① 维持领导者地位；② 防止出现 “幽灵复现” 问题；

<div style="text-align: center;"> 
    <img src="./pictures/leader_select.jpg" title="term" width="900" height="780">
</div>

### 5.4、Candidate 投票过程（RequestVote）
　　**当一个节点收到比自己任期更大的请求投票 RPC**。说明自己的状态更旧，或者没有抢先发起 leader 选举（节点性能或者网络原因）。这时应该更新自己的任期为请求投票 RPC 的任期，将 status 设置为 Follower（表明该节点竞争 leader 失败），并比较日志的新旧程度（网速快不代表包括全部的已提交日志）。如果自己的更新，则不投票给请求投票节点，否则给其投票。<br>

　　**当一个节点收到比自己任期更小的请求投票 RPC**。说明请求投票节点的状态存在问题，请求投票节点的任期自增 1 后还是小于自己，只有一种情况：请求投票节点没有实时同步集群的状态，可能是崩溃重启，或者网络延迟的问题。这时应该拒绝投票。<br>

　　**当一个节点收到和自己任期相等的请求投票 RPC**。说明当前节点和请求节点在竞争同一个任期内的 leader，这时需要判断自己是否已经给别人或者自己投过票，如果投过（自己或者别人），则拒绝投票，否则给其投票。<br>

<div style="text-align: center;"> 
    <img src="./pictures/example4.jpeg" title="term" width="580" height="550">
</div> 


### 5.5、日志复制（AppendEntries）
　　日志复制可以完全交给　heartbeat　周期来触发，即收到　command　后，并不立刻发送日志复制 RPC，而是等待下一个心跳。这种方法可以减少 RPC 的数量，但是代价就是每条 command 的提交周期变长。

#### 1、leader 发送追加日志 RPC 请求的流程
　　leader 发送完追加日志 RPC 后，会收到来自 follower 的响应，这时的响应有如下几种情况：<br>
　　① follower 的 term 大于 leader 的 term，说明 leader 已经过时，可能是崩溃后重启，这时需要将自己转为 follower，并更新自己的 term。<br>
　　② follower 的 term 小于 leader 的 term，说明 follower 已经过时，此时 leader 的日志肯定是在 follower 中不存在，这时更新 leader 的 nextIndex -= 1。<br>
　　③ follower 的 term 等于 leader 的 term，此时存在两种情况，日志复制成功和日志冲突。日志复制成功则可以更新 leader 的 nextIndex += len(本次复制的日志)。日志冲突则需要根据 follower 冲突的原因进行不同的处理，具体情况如下如所示：

<div style="text-align: center;"> 
    <img src="./pictures/example9.jpg" title="term" width="830" height="600">
</div> 


#### 2、follower 收到追加日志 RPC 请求的流程
　　① <span style="color: red;">**请求的任期 < 当前节点的任期**</span>，说明发送请求的节点的状态已经过时，拒绝请求。举例：S1 为 leader，发送了一个追加日志 RPC，由于网络延时，并在此期间 S1 崩溃了，同时选举出了新的 leader S2，此时任期已经发生变化，这时 leader S2 收到追加日志时，应该丢弃。<br>
　　② <span style="color: red;">**请求的任期 > 当前节点的任期**</span>，说明当前节点可能时从故障崩溃中刚恢复过来，此时更新自己的任期=请求的任期，并结束返回响应。<br>
　　③ <span style="color: red;">**请求的任期 == 当前节点的任期 && 待同步日志的上一条日志的 index > 当前节点最新日志的 index**</span>，说明当前节点没有待同步日志的上一条日志，也即日志缺失，结束并返回结果。<br>
　　④ <span style="color: red;">**请求的任期 == 当前节点的任期 && 当前节点包含待同步日志的上一条日志 && 两个日志的 term 不同**</span>，说明在某个时间点，当前节点和领导者节点的日志发生了分叉，即两者在同一个日志索引处存储了不同的日志条目。如下图所示，S3 在同步 index 3 的日志时，发现 index 2 的日志和 S1 不一致，此时需要将本节点 index 2 的日志进行覆盖，保持和 leader 一致。
<div style="text-align: center;"> 
    <img src="./pictures/example5.jpg" title="term" width="140" height="110">
</div> 

　　⑤ <span style="color: red;">**请求的任期 == 当前节点的任期 && 当前节点包含待同步日志的上一条日志 && 两个日志的 term 相同**</span>，说明可以开始进行日志复制了，但还需要注意日志截断。如下图所示，S3 在同步 index 3 的日志时，上一个节点相同，但仍需要将本节点 index 3 的日志进行覆盖，从而保持和 leader 一致。
<div style="text-align: center;"> 
    <img src="./pictures/example6.jpg" title="term" width="140" height="110">
</div> 

　　另外，实现时并不是每个日志都使用一个追加日志 RPC 来发送，而是 leader 中会保存每个 follower 中最新的日志的 index，然后将 leader 所有 index > 保存的 index 的日志统一一起打包发送给 follower。如下图中的蓝色方块，它们将会一起被发送至 S3。
<div style="text-align: center;"> 
    <img src="./pictures/example7.jpg" title="term" width="140" height="100">
</div> 

#### 3、Raft 的日志一致性检查优化
　　**问题：什么是 Raft 的一致性检查？<br>**
　　答：保证 follower 日志和 leader 日志一致的手段。leader 在每一个发往 follower 的追加条目 RPC 中，会放入前一个日志条目的索引位置和任期号，如果 follower 在它的日志中找不到前一个日志，那么它就会拒绝此日志，leader 收到 follower 的拒绝后，会发送前一个日志条目，从而逐渐向前定位到 follower 第一个缺失的日志。<br>

　　一个一个的往前找下一个应该复制给 follower 的日志是不是太慢了？所以在 follower 的拒绝响应中增加了 XTerm、XIndex 等相关字段。<span style="color: red;">领导者根据这个字段来找到其自身日志中与冲突任期相同的最后一个条目位置，以便调整 nextIndex，从而有效地解决冲突</span>。具体情况可见 3.5 中的第一部分。<br>

　　**优化算法逻辑如下：<br>**
　　① 如果一个 follower 的日志中没有 prevLogIndex，那么它应该返回 XIndex = len(log) 和 XTerm = None。<br>
　　② 如果一个 follower 的日志中有 prevLogIndex，但是对应的 term 不匹配，那么它应该返回 XTerm = log[prevLogIndex].Term，然后在它的日志中从左到右搜索第一个 term 等于 log[prevLogIndex].Term 的日志，并将其 index 设置给 XIndex。<br>
　　③ 当 leader 收到冲突响应时，首先应在其日志中搜索 XTerm 最后一次出现的位置。如果找到了，则设置 nextIndex 为该日志之后的一个的 index。<br>
　　④ 如果它在日志中找不到具有该 term 的条目，则应将 nextIndex 设置为 index。<br>

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

　　**问题：为什么要返回 XTerm 呢？只有 XIndex 不可以吗？<br>**
　　答：实际上是可以只用 XIndex 来实现优化，但加上 XTerm 优化效果更好。如下图所示：<br>
<div style="text-align: center;"> 
    <img src="./pictures/example12.jpg" title="term" width="260" height="90">
</div> 

　　① leader S1 发送心跳，其中 prevLogIndx 为 7，prevLogTerm 为 5。<br>
　　② follower S2 发现日志冲突，因为它节点中 index 为 7 的日志，term 是 4。按照优化算法返回给 leader 响应是：XIndex = 0（term 为 4 的第一条日志的下标），XTerm = 4。 <br>
　　③ 如果没有 XTerm，那么会下一次同步 nextIndex 为 0，会将 0~7 的日志全部同步。而如果有 XTerm ，根据 Raft 的日志匹配特性，下一次同步只需要传输 6~7 的日志。



### 5.6、日志提交
　　leader 在每次复制日志到 follower 时，会在 RPC 中携带当前已经提交的日志位置（commitIndex）。如果 follower 复制成功，则会更新它的提交日志位置为 min(args.LeaderCommit, rf.log.lastLog().Index)，位置前面的日志代表已提交。问题：为什么是取 min(args.LeaderCommit, rf.log.lastLog().Index) 呢？
<div style="text-align: center;"> 
    <img src="./pictures/example8.jpg" title="term" width="500" height="140">
</div> 
　　答：因为对于 LeaderCommit 和 lastLog().Index 存在上面两种可能，这两种可能在更新 follower 的提交位置时，都需要设置为二者的较小值。另外，因为 LeaderCommit < leader.lastLog().Index，同时日志复制后的 follower.lastLog().Index 是等于 leader.lastLog().Index 的，所以不可能出现 LeaderCommit > follower.lastLog().Index 的情况。<br>
　　日志提交后，follower 就可以 apply 应用该日志的命令到状态机了。每个 Raft 节点在启动时都会专门启动一个 Goroutine 来专门应用日志命令到状态机中。


### 5.7、持久化
　　目标：持久化 Raft 节点的 state，使节点重启时能够恢复数据。

　　**问题：需要持久化什么？<br>**
　　答：论文中的图 2 中进行了说明，分别是：<br>
　　① currentTerm：当前任期，这个肯定需要持久化。<br>
　　② voteFor：这里持久化的目的是，避免一次任期投两次票。<br>
　　③ logs：日志，重启回复需要重新执行一次命令。<br>

　　**问题：什么时候进行持久化? <br>**
　　答：当 currentTerm，votedFor，log 发生改变时。例如：<br>
　　① Start 函数中新增一条日志保存到状态机时。<br>
　　② leader 选举启动时，candidate 会增加 term 号。<br>
　　③ 收到追加日志 RPC 并确定需要将日志同步至节点中时，包含正常复制和截断两种情况。<br>
　　④ 节点之间 term 不同时，这时 term 小的需要和 term 大的保持一致，这可能发生在 RPC 的请求和响应中。<br>
　　⑤ 转变成 Follower，重置 voteFor 时。<br>
　　⑥ 给节点投票时。<br>

　　持久化真实的实现会在每次更改时将 Raft 的持久状态写入磁盘，并在重启后从磁盘读取状态。但因为是模拟实现，所以代码中会使用 Persister 对象（见 persister.go）来保存和恢复持久状态。具体是 Persister 的 ReadRaftState() 和 SaveRaftState() 方法。<br>
　　调用 Raft.Make() 的用户会提供一个最初包含 Raft 最近持久化状态（如果有）的 Persister。Raft 应该从这个 Persister 初始化其状态，并在每次状态更改时使用它来保存其持久状态。


### 5.8、checkOneLeader
　　检查集群中是否只存在一个 leader。此处循环 10 次的原因是：分布式系统中某时刻正在选举，可能没有 leader。
<div style="text-align: center;"> 
    <img src="./pictures/checkleaderone.jpg" title="term" width="400" height="400">
</div> 


## 六、测试项
### 6.1、TestInitialElection2A
　　① 集群启动后，leader 选举是否成功。<br>
　　② 网络正常的情况下，2 个选举超时时间后，集群的 term 是否会改变，leader 是否正常。

### 6.2、TestReElection2A
　　主要测试了 leader 崩溃和重新连接的相关 case。<br>
　　① leader 崩溃后，新 leader 的产生。<br>
　　② 旧的 leader 重新加入，不影响新 leader 的选举（因为收到比自己任期大的 RPC 时，自身状态会变成 Follower）。<br>
　　③ 如果有效机器数不足一半，不应该选出 leader。<br>
　　④ 如果法定人数恢复，应该选出一个 leader。<br>
　　⑤ 旧的 leader 重新加入，不影响 leader 的存在。<br>

### 6.3、TestManyElections2A
　　测试 Leader 选举的健壮性。即在反复的节点断开和连接过程中，集群能够持续选举出 Leader。

### 6.4、TestBasicAgree2B
　　测试 leader 在收到日志请求时，是否能够复制到 Follower 节点，而后是否能够正常提交。

### 6.5、TestRPCBytes2B
　　测试在向集群发送 10 次命令请求后，检查集群能够正常日志复制，并检查每个命名是否从 leader 发送至 follower 多次。

### 6.6、TestFailAgree2B
　　测试当某个节点断连后，集群是否还能够正常日志复制。<br>
　　测试断连节点重连恢复后，集群是否还能够正常日志复制。<br>

### 6.7、TestFailNoAgree2B
　　测试了当大多数断开连接时，日志复制后不应该提交。 <br>
　　验证了断开的 follower 重新连接后，系统是否能够重新选举 leader。<br>
　　验证了新的领导者是否可以正常提交新的日志条目。<br>

### 6.8、TestConcurrentStarts2B
　　测试 Raft 集群在并发环境下的稳定性和一致性，特别是当多个并发请求（5 个）同时向 leader 提交时，系统能否正确处理这些请求。

### 6.9、TestRejoin2B
　　测试当 leader 被网络分区后，再次加入集群时，系统能否正确进行日志复制和提交。

### 6.10、TestBackup2B
　　测试领导者在日志不一致的情况下，能够快速回滚（backup）并使其日志与集群其他成员的日志保持一致。
<div style="text-align: center;"> 
    <img src="./pictures/test_rejoin2B.jpg" title="term" width="960" height="600">
</div> 

### 6.11、TestCount2B
　　测试初始领导者选举的 RPC 调用次数是否合理。<br>
　　测试日志提交时，RPC 调用次数是否合理。<br>
　　测试在集群空闲时，RPC 调用次数是否合理。<br>

### 6.12、TestPersist12C
　　测试了服务器在崩溃和重新启动后是否能正确恢复并继续工作。




## 参考链接：

- https://github.com/s09g/raft-go/tree/main
- https://www.bilibili.com/video/BV1pr4y1b7H5/?spm_id_from=333.337.search-card.all.click&vd_source=ff9932351ef409bb94e9586a7891b82e
- https://www.bilibili.com/video/BV1CQ4y1r7qf/?spm_id_from=333.337.search-card.all.click&vd_source=ff9932351ef409bb94e9586a7891b82e
- https://pdos.csail.mit.edu/6.824/papers/raft-extended.pdf
- https://thesquareplanet.com/blog/students-guide-to-raft/#an-aside-on-optimizations
- https://github.com/he2121/MIT6.824-2021
- https://blog.csdn.net/weixin_45938441/article/details/125179308


