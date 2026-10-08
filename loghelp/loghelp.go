package loghelp

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 日志级别常量
const (
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// Logger 日志结构体（按天拆分 + 大小兜底）
type Logger struct {
	baseName    string      // 日志基础文件名（如 "scttool.log"）
	maxSize     int64       // 单个日志文件最大大小（字节），超限则按时间戳备份
	file        *os.File    // 当前日志文件句柄
	currentSize int64       // 当前文件大小
	currentDate string      // 当前文件对应的日期（yyyy-MM-dd），跨天则切换
	consoleMode bool        // true=console运行（输出到控制台+文件），false=service运行（仅写文件）
	openFailed  bool        // 上次打开文件失败，下次 writeLog 时重试
	mu          sync.Mutex  // 写入互斥（保证并发安全）
}

// NewLogger 初始化日志类
// baseName: 日志基础文件名（如 "scttool.log"）
// maxSizeMB: 单个日志文件最大大小(MB)，超限则按时间戳备份
func NewLogger(baseName string, maxSizeMB int64) *Logger {
	maxSize := maxSizeMB * 1024 * 1024
	logger := &Logger{
		baseName:    baseName,
		maxSize:     maxSize,
		consoleMode: true, // 默认 console 模式
	}
	logger.openFile()
	return logger
}

// SetConsoleMode 设置运行模式
// true=console运行（输出到控制台+文件），false=service运行（仅写文件）
func (l *Logger) SetConsoleMode(isConsole bool) {
	l.consoleMode = isConsole
}

// logFileName 根据日期生成日志文件名
// 格式：baseName.yyyy-MM-dd.log（如 scttool.2026-08-12.log）
func (l *Logger) logFileName(dateStr string) string {
	dir := filepath.Dir(l.baseName)
	base := filepath.Base(l.baseName)
	// 如果 baseName 本身带扩展名（如 scttool.log），去掉扩展名再拼日期
	ext := filepath.Ext(base)
	nameWithoutExt := base
	if ext != "" {
		nameWithoutExt = base[:len(base)-len(ext)]
	}
	return filepath.Join(dir, fmt.Sprintf("%s.%s%s", nameWithoutExt, dateStr, ext))
}

// openFile 打开当天的日志文件
func (l *Logger) openFile() {
	today := time.Now().Format("2006-01-02")
	fileName := l.logFileName(today)

	file, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开日志文件失败: %v\n", err)
		l.openFailed = true
		return
	}

	stat, err := file.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取文件信息失败: %v\n", err)
		_ = file.Close() // 防止句柄泄漏
		l.openFailed = true
		return
	}

	l.file = file
	l.currentSize = stat.Size()
	l.currentDate = today
	l.openFailed = false
}

// closeFile 关闭当前日志文件
func (l *Logger) closeFile() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// checkDate 检查是否跨天，跨天则切换到新文件
func (l *Logger) checkDate() {
	today := time.Now().Format("2006-01-02")
	if l.currentDate != today {
		l.closeFile()
		l.openFile()
	}
}

// checkSize 检查文件大小，超限则备份当前文件并新建
func (l *Logger) checkSize() {
	if l.currentSize >= l.maxSize {
		// 备份：文件名追加时间戳（精确到秒）
		now := time.Now().Format("20060102150405")
		oldName := l.logFileName(l.currentDate)
		backupName := fmt.Sprintf("%s.%s", oldName, now)
		l.closeFile()
		if err := os.Rename(oldName, backupName); err != nil {
			fmt.Fprintf(os.Stderr, "日志文件备份重命名失败: %v\n", err)
		}
		l.openFile()
	}
}

// getLogPrefix 获取日志前缀（级别+时间，tab 分隔）
func (l *Logger) getLogPrefix(level string) string {
	now := time.Now().Format("2006-01-02 15:04:05.000")
	return fmt.Sprintf("[%s]\t[%s]\t", level, now)
}

// writeLog 写入日志（线程安全）
// toConsole: 调用方是否希望输出到控制台（实际是否输出还受 consoleMode 控制）
// msg: 日志内容
func (l *Logger) writeLog(level string, toConsole bool, msg string) {
	prefix := l.getLogPrefix(level)
	logMsg := prefix + msg + "\n"

	// 控制台输出：仅在 console 模式且调用方要求时输出
	if l.consoleMode && toConsole {
		fmt.Print(logMsg)
	}

	// 文件输出
	l.mu.Lock()
	defer l.mu.Unlock()

	// 上次打开失败，尝试重新打开
	if l.openFailed {
		l.openFile()
	}

	if l.file == nil {
		return
	}

	// 检查跨天
	l.checkDate()

	// 写入文件
	n, err := l.file.WriteString(logMsg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "写入日志失败: %v\n", err)
	}
	l.currentSize += int64(n)

	// 检查大小
	l.checkSize()
}

// Info 信息日志
// toConsole: true=打印控制台，false=仅写文件
func (l *Logger) Info(toConsole bool, msg string) {
	l.writeLog(LevelInfo, toConsole, msg)
}

// Warn 警告日志
func (l *Logger) Warn(toConsole bool, msg string) {
	l.writeLog(LevelWarn, toConsole, msg)
}

// Error 错误日志
func (l *Logger) Error(toConsole bool, msg string) {
	l.writeLog(LevelError, toConsole, msg)
}

// Infof 信息日志（格式化，遵循 consoleMode）
func (l *Logger) Infof(format string, args ...interface{}) {
	l.writeLog(LevelInfo, true, fmt.Sprintf(format, args...))
}

// Warnf 警告日志（格式化，遵循 consoleMode）
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.writeLog(LevelWarn, true, fmt.Sprintf(format, args...))
}

// Errorf 错误日志（格式化，遵循 consoleMode）
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.writeLog(LevelError, true, fmt.Sprintf(format, args...))
}

// Close 关闭日志文件句柄
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeFile()
}

// CheckWritable 检查日志文件是否可写
// 尝试打开当天的日志文件并写入一条空内容，验证写入权限
func (l *Logger) CheckWritable() error {
	today := time.Now().Format("2006-01-02")
	fileName := l.logFileName(today)

	// 如果文件句柄已打开，直接尝试刷盘验证
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		if err := l.file.Sync(); err != nil {
			return fmt.Errorf("日志文件刷盘失败: %v", err)
		}
		return nil
	}

	// 否则尝试打开并刷盘验证
	file, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("日志文件打开失败: %v", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("日志文件刷盘失败: %v", err)
	}
	// 预检通过，让 logger 重新获取文件句柄
	l.openFile()
	return nil
}