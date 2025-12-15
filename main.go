package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/ethclient"
	"gopkg.in/yaml.v3"
)

// 配置结构体定义
type Config struct {
	Network     NetworkConfig     `yaml:"network"`
	Contract    ContractConfig    `yaml:"contract"`
	Monitoring  MonitoringConfig  `yaml:"monitoring"`
	Output      OutputConfig      `yaml:"output"`
	Debug       DebugConfig       `yaml:"debug"`
	Performance PerformanceConfig `yaml:"performance"`
	Filters     FiltersConfig     `yaml:"filters"`
}

type NetworkConfig struct {
	Name              string        `yaml:"name"`
	WssURL            string        `yaml:"wss_url"`
	FallbackURLs      []string      `yaml:"fallback_urls"`
	ChainID           int           `yaml:"chain_id"`
	ReconnectInterval time.Duration `yaml:"reconnect_interval"`
	Timeout           time.Duration `yaml:"timeout"`
}

type ContractConfig struct {
	Address string `yaml:"address"`
	ABIFile string `yaml:"abi_file"`
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type MonitoringConfig struct {
	StartBlock string        `yaml:"start_block"`
	BatchSize  int           `yaml:"batch_size"`
	Events     []EventConfig `yaml:"events"`
}

type EventConfig struct {
	Name        string `yaml:"name"`
	Enabled     bool   `yaml:"enabled"`
	Description string `yaml:"description"`
	Priority    string `yaml:"priority"`
}

type OutputConfig struct {
	Console ConsoleOutput `yaml:"console"`
	File    FileOutput    `yaml:"file"`
}

type ConsoleOutput struct {
	Enabled       bool   `yaml:"enabled"`
	Format        string `yaml:"format"`
	ShowTimestamp bool   `yaml:"show_timestamp"`
	ShowBlockInfo bool   `yaml:"show_block_info"`
	ShowTxHash    bool   `yaml:"show_tx_hash"`
	Color         bool   `yaml:"color"`
}

type FileOutput struct {
	Enabled  bool   `yaml:"enabled"`
	Path     string `yaml:"path"`
	Rotation string `yaml:"rotation"`
	MaxSize  string `yaml:"max_size"`
}

type DebugConfig struct {
	Enabled             bool `yaml:"enabled"`
	LogRawEvents        bool `yaml:"log_raw_events"`
	ShowEventSignatures bool `yaml:"show_event_signatures"`
	Verbose             bool `yaml:"verbose"`
}

type PerformanceConfig struct {
	MaxConnections int `yaml:"max_connections"`
	BufferSize     int `yaml:"buffer_size"`
	WorkerCount    int `yaml:"worker_count"`
}

type FiltersConfig struct {
	TokenAddresses        []string   `yaml:"token_addresses"`
	MinTransactionValue   float64    `yaml:"min_transaction_value"`
	TimeFilter           TimeFilter `yaml:"time_filter"`
}

type TimeFilter struct {
	Enabled   bool   `yaml:"enabled"`
	StartTime string `yaml:"start_time"`
	EndTime   string `yaml:"end_time"`
}

// 事件结构体定义 (基于TokenManager2 V2 ABI)
type TokenCreateEvent struct {
	Creator     common.Address
	Token       common.Address
	RequestId   *big.Int
	Name        string
	Symbol      string
	TotalSupply *big.Int
	LaunchTime  *big.Int
	LaunchFee   *big.Int
}

type TokenPurchaseEvent struct {
	Token   common.Address
	Account common.Address
	Price   *big.Int
	Amount  *big.Int
	Cost    *big.Int
	Fee     *big.Int
	Offers  *big.Int
	Funds   *big.Int
}

type TokenSaleEvent struct {
	Token   common.Address
	Account common.Address
	Price   *big.Int
	Amount  *big.Int
	Cost    *big.Int
	Fee     *big.Int
	Offers  *big.Int
	Funds   *big.Int
}

type LiquidityAddedEvent struct {
	Base   common.Address
	Offers *big.Int
	Quote  common.Address
	Funds  *big.Int
}

type TradeStopEvent struct {
	Token common.Address
}

type TokenPurchase2Event struct {
	Origin *big.Int
}

type TokenSale2Event struct {
	Origin *big.Int
}

// ABI加载函数
func loadABI(abiPath string) (abi.ABI, error) {
	fmt.Printf("📄 正在加载ABI文件: %s\n", abiPath)
	
	// 读取ABI文件
	abiBytes, err := os.ReadFile(abiPath)
	if err != nil {
		return abi.ABI{}, fmt.Errorf("读取ABI文件失败: %v", err)
	}
	
	// 解析ABI
	contractABI, err := abi.JSON(strings.NewReader(string(abiBytes)))
	if err != nil {
		return abi.ABI{}, fmt.Errorf("解析ABI失败: %v", err)
	}
	
	// 统计ABI中的事件数量
	eventCount := len(contractABI.Events)
	fmt.Printf("✅ ABI加载成功! 发现 %d 个事件\n", eventCount)
	
	// 列出所有事件名称
	if eventCount > 0 {
		fmt.Printf("🎯 ABI中的事件列表:\n")
		for eventName := range contractABI.Events {
			fmt.Printf("   - %s\n", eventName)
		}
	}
	
	return contractABI, nil
}

// 获取启用的事件映射
func getEnabledEventMap(config *Config) map[string]bool {
	eventMap := make(map[string]bool)
	for _, event := range config.Monitoring.Events {
		eventMap[event.Name] = event.Enabled
	}
	return eventMap
}

// 验证配置中的事件在ABI中是否存在
func validateEvents(config *Config, contractABI abi.ABI) error {
	fmt.Println("🔍 验证事件配置...")
	
	validEvents := 0
	for _, eventConfig := range config.Monitoring.Events {
		if _, exists := contractABI.Events[eventConfig.Name]; exists {
			if eventConfig.Enabled {
				fmt.Printf("   ✅ %s - 已启用\n", eventConfig.Name)
			} else {
				fmt.Printf("   ⚪ %s - 已禁用\n", eventConfig.Name)
			}
			validEvents++
		} else {
			fmt.Printf("   ❌ %s - 在ABI中未找到!\n", eventConfig.Name)
		}
	}
	
	fmt.Printf("📊 事件验证完成: %d/%d 事件在ABI中找到\n", validEvents, len(config.Monitoring.Events))
	
	if validEvents == 0 {
		return fmt.Errorf("没有找到任何有效的事件")
	}
	
	return nil
}

// 配置加载函数
func loadConfig() (*Config, error) {
	fmt.Println("📄 正在加载配置文件: config.yaml")
	
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}
	
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析YAML配置失败: %v", err)
	}
	
	// 基础验证
	if config.Network.WssURL == "" {
		return nil, fmt.Errorf("network.wss_url 不能为空")
	}
	if config.Contract.Address == "" {
		return nil, fmt.Errorf("contract.address 不能为空")
	}
	if config.Contract.ABIFile == "" {
		return nil, fmt.Errorf("contract.abi_file 不能为空")
	}
	
	return &config, nil
}

// 尝试连接单个WSS端点
func tryConnectWSS(url string, config *Config) (*ethclient.Client, error) {
	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(context.Background(), config.Network.Timeout)
	defer cancel()
	
	// 连接到WSS端点
	client, err := ethclient.DialContext(ctx, url)
	if err != nil {
		return nil, err
	}
	
	// 测试连接 - 获取链ID
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		client.Close()
		return nil, err
	}
	
	// 验证链ID是否正确
	if chainID.Uint64() != uint64(config.Network.ChainID) {
		client.Close()
		return nil, fmt.Errorf("链ID不匹配: 期望%d, 实际%d", 
			config.Network.ChainID, chainID.Uint64())
	}
	
	// 获取最新区块号测试连接
	latestBlock, err := client.BlockNumber(context.Background())
	if err != nil {
		client.Close()
		return nil, err
	}
	
	fmt.Printf("✅ WSS连接成功!\n")
	fmt.Printf("🌍 节点URL: %s\n", url)
	fmt.Printf("⛓️  链ID: %d\n", chainID.Uint64())
	fmt.Printf("📦 最新区块: #%d\n", latestBlock)
	
	return client, nil
}

// WSS连接函数（支持备用节点）
func connectWSS(config *Config) (*ethclient.Client, error) {
	fmt.Printf("🔗 正在连接到: %s\n", config.Network.Name)
	
	// 准备所有要尝试的URL
	urls := []string{config.Network.WssURL}
	urls = append(urls, config.Network.FallbackURLs...)
	
	var lastErr error
	
	// 依次尝试每个URL
	for i, url := range urls {
		if i == 0 {
			fmt.Printf("📡 尝试主节点: %s\n", url)
		} else {
			fmt.Printf("📡 尝试备用节点 %d: %s\n", i, url)
		}
		
		client, err := tryConnectWSS(url, config)
		if err != nil {
			fmt.Printf("❌ 连接失败: %v\n", err)
			lastErr = err
			continue
		}
		
		// 连接成功
		return client, nil
	}
	
	return nil, fmt.Errorf("所有WSS节点连接失败，最后错误: %v", lastErr)
}

// 基础监听框架
func startEventMonitor(client *ethclient.Client, config *Config) {
	fmt.Println("\n🎯 启动事件监听器...")
	fmt.Printf("📄 合约地址: %s\n", config.Contract.Address)
	
	// 统计启用的事件
	enabledEvents := []string{}
	for _, event := range config.Monitoring.Events {
		if event.Enabled {
			enabledEvents = append(enabledEvents, event.Name)
		}
	}
	
	fmt.Printf("📊 监听事件: %v\n", enabledEvents)
	fmt.Printf("🚀 监听状态: 准备就绪 (ABI加载将在步骤5实现)\n")
	
	// 在这个演示阶段，我们只是保持连接活跃
	fmt.Println("💤 保持WSS连接活跃，等待中断信号...")
	
	// 创建一个简单的心跳检查
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// 简单的连接健康检查
			if client == nil {
				fmt.Println("❌ WSS连接丢失")
				return
			}
			
			// 获取最新区块号作为连接测试
			blockNum, err := client.BlockNumber(context.Background())
			if err != nil {
				fmt.Printf("⚠️  连接检查失败: %v\n", err)
			} else {
				if config.Debug.Verbose {
					fmt.Printf("💓 连接健康检查: 当前区块 #%d\n", blockNum)
				}
			}
		}
	}
}

// 格式化原始wei值
func formatRawWei(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return value.String()
}

// 格式化BNB数值（原始wei）
func formatBNB(value *big.Int) string {
	if value == nil {
		return "0 wei"
	}
	return formatRawWei(value) + " wei"
}

// 格式化代币数值（原始最小单位）
func formatTokenAmount(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return formatRawWei(value)
}

// 获取毫秒级时间戳
func getCurrentTimestampMs() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

// 完整解码事件数据
func decodeEventData(vlog types.Log, eventABI abi.Event) (map[string]interface{}, error) {
	// 解码indexed和non-indexed参数
	var indexed abi.Arguments
	var nonIndexed abi.Arguments
	
	for _, input := range eventABI.Inputs {
		if input.Indexed {
			indexed = append(indexed, input)
		} else {
			nonIndexed = append(nonIndexed, input)
		}
	}
	
	// 解码数据
	values := make(map[string]interface{})
	
	// 解码indexed参数 (跳过第一个topic，它是事件签名)
	if len(vlog.Topics) > 1 && len(indexed) > 0 {
		// 将topics转换为字节数组
		var topicsBytes [][]byte
		for _, topic := range vlog.Topics[1:] {
			topicsBytes = append(topicsBytes, topic.Bytes())
		}
		
		// 逐个解码indexed参数
		for i, arg := range indexed {
			if i < len(topicsBytes) {
				value, err := abi.Arguments{arg}.Unpack(topicsBytes[i])
				if err != nil {
					return nil, fmt.Errorf("解码indexed参数 %s 失败: %v", arg.Name, err)
				}
				if len(value) > 0 {
					values[arg.Name] = value[0]
				}
			}
		}
	}
	
	// 解码non-indexed参数
	if len(vlog.Data) > 0 && len(nonIndexed) > 0 {
		err := nonIndexed.UnpackIntoMap(values, vlog.Data)
		if err != nil {
			return nil, fmt.Errorf("解码data参数失败: %v", err)
		}
	}
	
	return values, nil
}

// 获取事件签名映射
func getEventSignatures(contractABI abi.ABI, enabledEvents map[string]bool) map[common.Hash]string {
	signatures := make(map[common.Hash]string)
	
	for eventName, abiEvent := range contractABI.Events {
		if enabled, exists := enabledEvents[eventName]; exists && enabled {
			signature := abiEvent.ID
			signatures[signature] = eventName
			if enabledEvents["Debug"] { // 使用配置中的debug标志
				fmt.Printf("🔑 事件签名: %s -> %s\n", eventName, signature.Hex())
			}
		}
	}
	
	return signatures
}

// 解析事件日志
func parseEventLog(vlog types.Log, contractABI abi.ABI, eventSignatures map[common.Hash]string) {
	// 检查是否是我们关心的事件
	eventName, exists := eventSignatures[vlog.Topics[0]]
	if !exists {
		return
	}
	
	// 获取事件ABI定义
	eventABI, exists := contractABI.Events[eventName]
	if !exists {
		fmt.Printf("⚠️  未找到事件ABI定义: %s\n", eventName)
		return
	}
	
	// 解析事件数据
	fmt.Printf("\n🎉 检测到事件: %s\n", eventName)
	fmt.Printf("⏰ 本地时间: %s\n", getCurrentTimestampMs())
	fmt.Printf("📦 区块: #%d\n", vlog.BlockNumber)
	fmt.Printf("🏷️  交易: %s\n", vlog.TxHash.Hex())
	fmt.Printf("📍 日志索引: %d\n", vlog.Index)
	
	// 根据不同事件类型进行详细解析
	switch eventName {
	case "TokenCreate":
		parseTokenCreateEvent(vlog, eventABI)
	case "TokenPurchase":
		parseTokenPurchaseEvent(vlog, eventABI)
	case "TokenSale":
		parseTokenSaleEvent(vlog, eventABI)
	case "LiquidityAdded":
		parseLiquidityAddedEvent(vlog, eventABI)
	case "TradeStop":
		parseTradeStopEvent(vlog, eventABI)
	default:
		fmt.Printf("📋 事件数据: %d topics, %d bytes data\n", len(vlog.Topics), len(vlog.Data))
	}
	
	fmt.Println(strings.Repeat("-", 50))
}

// 解析TokenCreate事件
func parseTokenCreateEvent(vlog types.Log, eventABI abi.Event) {
	fmt.Println("📈 代币创建事件:")
	
	// 解码完整的事件数据
	values, err := decodeEventData(vlog, eventABI)
	if err != nil {
		fmt.Printf("❌ 数据解码失败: %v\n", err)
		return
	}
	
	// 显示详细信息
	if creator, ok := values["creator"].(common.Address); ok {
		fmt.Printf("👤 创建者: %s\n", creator.Hex())
	}
	if token, ok := values["token"].(common.Address); ok {
		fmt.Printf("🪙 代币地址: %s\n", token.Hex())
	}
	if requestId, ok := values["requestId"].(*big.Int); ok {
		fmt.Printf("🆔 请求ID: %s\n", requestId.String())
	}
	if name, ok := values["name"].(string); ok {
		fmt.Printf("📛 代币名称: %s\n", name)
	}
	if symbol, ok := values["symbol"].(string); ok {
		fmt.Printf("🔤 代币符号: %s\n", symbol)
	}
	if totalSupply, ok := values["totalSupply"].(*big.Int); ok {
		fmt.Printf("📊 总供应量: %s\n", formatTokenAmount(totalSupply))
	}
	if launchTime, ok := values["launchTime"].(*big.Int); ok {
		launchUnix := launchTime.Int64()
		launchTimeFormatted := time.Unix(launchUnix, 0).Format("2006-01-02 15:04:05")
		fmt.Printf("🚀 启动时间: %s\n", launchTimeFormatted)
	}
	if launchFee, ok := values["launchFee"].(*big.Int); ok {
		fmt.Printf("💰 启动费用: %s\n", formatBNB(launchFee))
	}
}

// 解析TokenPurchase事件
func parseTokenPurchaseEvent(vlog types.Log, eventABI abi.Event) {
	fmt.Println("💰 代币购买事件:")
	
	// 解码完整的事件数据
	values, err := decodeEventData(vlog, eventABI)
	if err != nil {
		fmt.Printf("❌ 数据解码失败: %v\n", err)
		return
	}
	
	// 显示详细信息
	if token, ok := values["token"].(common.Address); ok {
		fmt.Printf("🪙 代币地址: %s\n", token.Hex())
	}
	if account, ok := values["account"].(common.Address); ok {
		fmt.Printf("👤 购买者: %s\n", account.Hex())
	}
	if price, ok := values["price"].(*big.Int); ok {
		fmt.Printf("💵 价格: %s\n", formatBNB(price))
	}
	if amount, ok := values["amount"].(*big.Int); ok {
		fmt.Printf("📊 购买数量: %s\n", formatTokenAmount(amount))
	}
	if cost, ok := values["cost"].(*big.Int); ok {
		fmt.Printf("💰 总成本: %s\n", formatBNB(cost))
	}
	if fee, ok := values["fee"].(*big.Int); ok {
		fmt.Printf("🏷️  手续费: %s\n", formatBNB(fee))
	}
	if offers, ok := values["offers"].(*big.Int); ok {
		fmt.Printf("📈 剩余供应: %s\n", formatTokenAmount(offers))
	}
	if funds, ok := values["funds"].(*big.Int); ok {
		fmt.Printf("💎 累计资金: %s\n", formatBNB(funds))
	}
}

// 解析TokenSale事件
func parseTokenSaleEvent(vlog types.Log, eventABI abi.Event) {
	fmt.Println("💸 代币出售事件:")
	
	// 解码完整的事件数据
	values, err := decodeEventData(vlog, eventABI)
	if err != nil {
		fmt.Printf("❌ 数据解码失败: %v\n", err)
		return
	}
	
	// 显示详细信息
	if token, ok := values["token"].(common.Address); ok {
		fmt.Printf("🪙 代币地址: %s\n", token.Hex())
	}
	if account, ok := values["account"].(common.Address); ok {
		fmt.Printf("👤 出售者: %s\n", account.Hex())
	}
	if price, ok := values["price"].(*big.Int); ok {
		fmt.Printf("💵 价格: %s\n", formatBNB(price))
	}
	if amount, ok := values["amount"].(*big.Int); ok {
		fmt.Printf("📊 出售数量: %s\n", formatTokenAmount(amount))
	}
	if cost, ok := values["cost"].(*big.Int); ok {
		fmt.Printf("💰 获得资金: %s\n", formatBNB(cost))
	}
	if fee, ok := values["fee"].(*big.Int); ok {
		fmt.Printf("🏷️  手续费: %s\n", formatBNB(fee))
	}
	if offers, ok := values["offers"].(*big.Int); ok {
		fmt.Printf("📈 剩余供应: %s\n", formatTokenAmount(offers))
	}
	if funds, ok := values["funds"].(*big.Int); ok {
		fmt.Printf("💎 累计资金: %s\n", formatBNB(funds))
	}
}

// 解析LiquidityAdded事件
func parseLiquidityAddedEvent(vlog types.Log, eventABI abi.Event) {
	fmt.Println("🌊 流动性添加事件:")
	
	// 解码完整的事件数据
	values, err := decodeEventData(vlog, eventABI)
	if err != nil {
		fmt.Printf("❌ 数据解码失败: %v\n", err)
		return
	}
	
	// 显示详细信息
	if base, ok := values["base"].(common.Address); ok {
		fmt.Printf("📊 基础代币: %s\n", base.Hex())
	}
	if offers, ok := values["offers"].(*big.Int); ok {
		fmt.Printf("🎯 代币数量: %s\n", formatTokenAmount(offers))
	}
	if quote, ok := values["quote"].(common.Address); ok {
		if quote.Hex() == "0x0000000000000000000000000000000000000000" {
			fmt.Printf("💱 交易对: BNB\n")
		} else {
			fmt.Printf("💱 报价代币: %s\n", quote.Hex())
		}
	}
	if funds, ok := values["funds"].(*big.Int); ok {
		fmt.Printf("💰 资金量: %s\n", formatBNB(funds))
	}
}

// 解析TradeStop事件
func parseTradeStopEvent(vlog types.Log, eventABI abi.Event) {
	fmt.Println("🛑 交易停止事件:")
	
	// 解码完整的事件数据
	values, err := decodeEventData(vlog, eventABI)
	if err != nil {
		fmt.Printf("❌ 数据解码失败: %v\n", err)
		return
	}
	
	// 显示详细信息
	if token, ok := values["token"].(common.Address); ok {
		fmt.Printf("🪙 代币地址: %s\n", token.Hex())
		fmt.Printf("⚠️  状态: 交易已暂停\n")
	}
}

// 实时事件监听
func startRealTimeEventListener(client *ethclient.Client, config *Config, contractABI abi.ABI) {
	fmt.Println("🎯 启动实时事件监听...")
	
	// 获取启用的事件
	enabledEvents := getEnabledEventMap(config)
	eventSignatures := getEventSignatures(contractABI, enabledEvents)
	
	fmt.Printf("📊 监听签名: %d个事件\n", len(eventSignatures))
	
	// 创建事件过滤查询
	contractAddress := common.HexToAddress(config.Contract.Address)
	query := ethereum.FilterQuery{
		Addresses: []common.Address{contractAddress},
	}
	
	// 订阅日志
	fmt.Println("📡 订阅合约事件日志...")
	logs := make(chan types.Log)
	
	sub, err := client.SubscribeFilterLogs(context.Background(), query, logs)
	if err != nil {
		log.Printf("❌ 事件订阅失败: %v", err)
		return
	}
	defer sub.Unsubscribe()
	
	fmt.Println("✅ 事件订阅成功! 等待事件...")
	fmt.Printf("🎯 监听合约: %s\n", config.Contract.Address)
	
	// 监听循环
	for {
		select {
		case err := <-sub.Err():
			fmt.Printf("❌ 订阅错误: %v\n", err)
			return
			
		case vlog := <-logs:
			// 解析事件日志
			parseEventLog(vlog, contractABI, eventSignatures)
		}
	}
}

// 带ABI支持的监听框架
func startEventMonitorWithABI(client *ethclient.Client, config *Config, contractABI abi.ABI) {
	fmt.Println("\n🎯 启动ABI集成的事件监听器...")
	fmt.Printf("📄 合约地址: %s\n", config.Contract.Address)
	
	// 获取启用的事件列表
	enabledEventMap := getEnabledEventMap(config)
	
	// 显示事件状态
	fmt.Println("📊 事件监听状态:")
	enabledCount := 0
	for eventName, enabled := range enabledEventMap {
		if _, exists := contractABI.Events[eventName]; exists {
			if enabled {
				fmt.Printf("   ✅ %s - 已启用并就绪\n", eventName)
				enabledCount++
			} else {
				fmt.Printf("   ⚪ %s - 已禁用\n", eventName)
			}
		}
	}
	
	fmt.Printf("🎯 将监听 %d 个启用的事件\n", enabledCount)
	fmt.Println("🚀 开始实时事件监听...")
	
	// 启动实时事件监听
	go startRealTimeEventListener(client, config, contractABI)
	
	// 保持主程序运行并提供心跳监控
	fmt.Println("💤 主监听器运行中...")
	
	ticker := time.NewTicker(60 * time.Second) // 延长到60秒
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			if client == nil {
				fmt.Println("❌ WSS连接丢失")
				return
			}
			
			// 连接健康检查
			blockNum, err := client.BlockNumber(context.Background())
			if err != nil {
				fmt.Printf("⚠️  连接检查失败: %v\n", err)
			} else {
				if config.Debug.Verbose {
					fmt.Printf("💓 系统健康: 区块 #%d, 监听 %d 事件\n", blockNum, enabledCount)
				}
			}
		}
	}
}

// 优雅关闭处理
func setupGracefulShutdown(client *ethclient.Client) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-c
		fmt.Println("\n🛑 收到中断信号，正在关闭...")
		
		if client != nil {
			client.Close()
			fmt.Println("✅ WSS连接已关闭")
		}
		
		fmt.Println("👋 程序退出")
		os.Exit(0)
	}()
}

func main() {
	fmt.Println("🚀 BSC事件监听器 - 步骤7: 完整事件数据解码")
	
	// 第1步：加载配置文件
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("❌ 配置加载失败: %v", err)
	}
	
	// 显示配置信息
	fmt.Printf("✅ 配置加载成功: %s\n", config.Network.Name)
	
	// 统计启用的事件
	enabledEvents := 0
	for _, event := range config.Monitoring.Events {
		if event.Enabled {
			enabledEvents++
		}
	}
	fmt.Printf("📊 事件监听: %d个事件已启用\n", enabledEvents)
	
	// 第2步：加载和验证ABI
	fmt.Println("\n" + strings.Repeat("=", 30) + " ABI处理 " + strings.Repeat("=", 30))
	contractABI, err := loadABI(config.Contract.ABIFile)
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	
	// 验证配置中的事件
	err = validateEvents(config, contractABI)
	if err != nil {
		log.Fatalf("❌ 事件验证失败: %v", err)
	}
	
	// 第3步：建立WSS连接
	fmt.Println("\n" + strings.Repeat("=", 30) + " WSS连接 " + strings.Repeat("=", 30))
	client, err := connectWSS(config)
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	defer client.Close()
	
	// 第4步：设置优雅关闭
	setupGracefulShutdown(client)
	
	// 第5步：启动ABI集成的监听框架
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("🎉 ABI加载成功! 事件解析就绪!")
	fmt.Printf("📋 合约地址: %s\n", config.Contract.Address)
	fmt.Printf("📄 ABI文件: %s\n", config.Contract.ABIFile)
	fmt.Println("📝 下一步将在步骤6中实现事件订阅逻辑")
	fmt.Println("💡 按 Ctrl+C 退出程序")
	fmt.Println(strings.Repeat("=", 50))
	
	// 启动监听器 (现在包含ABI支持)
	startEventMonitorWithABI(client, config, contractABI)
}