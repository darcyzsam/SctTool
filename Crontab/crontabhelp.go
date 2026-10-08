package sctcrontab

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"regexp"
)

// 预编译正则（避免每次调用重新编译）
var (
	fieldPattern = regexp.MustCompile(`^(\*(\/\d+)?|(\d+(-\d+)?(\/\d+)?))(,\d+(-\d+)?(\/\d+)?)*$`)
	numSplitPattern = regexp.MustCompile(`[/*,-]`)
)

// CrontabParsed 解析后的crontab表达式结构
type CrontabParsed struct {
	Minute  []int
	Hour    []int
	Day     []int
	Month   []int
	Weekday []int
}

// ParseCrontab 解析crontab表达式（主函数）
// 返回：是否成功、错误信息、解析结果
func ParseCrontab(crontabStr string) (bool, string, *CrontabParsed) {
	// 先校验格式
	valid, errMsg := validateCrontabFormat(crontabStr)
	if !valid {
		return false, errMsg, nil
	}

	// 分割字段
	fields := filterEmpty(strings.Fields(crontabStr))
	if len(fields) != 5 {
		return false, fmt.Sprintf("字段数量错误，需为5个（分 时 日 月 周），实际为%d个", len(fields)), nil
	}

	minuteStr, hourStr, dayStr, monthStr, weekdayStr := fields[0], fields[1], fields[2], fields[3], fields[4]

	// 解析各字段
	parseField := func(field string, minVal, maxVal int) ([]int, error) {
		if field == "*" {
			return generateRange(minVal, maxVal, 1), nil
		}

		values := make(map[int]struct{})
		parts := strings.Split(field, ",")

		for _, part := range parts {
			step := 1
			// 处理步长
			if strings.Contains(part, "/") {
				stepParts := strings.Split(part, "/")
				if len(stepParts) != 2 {
					return nil, fmt.Errorf("步长格式非法：%s", part)
				}
				part = stepParts[0]
				stepInt, err := strconv.Atoi(stepParts[1])
				if err != nil {
					return nil, fmt.Errorf("步长值非法：%s（必须是数字）", stepParts[1])
				}
				if stepInt < 1 {
					return nil, fmt.Errorf("步长值非法：%d（必须大于0）", stepInt)
				}
				step = stepInt
			}

			// 处理范围或通配
			if part == "*" {
				// */step 格式
				rangeVals := generateRange(minVal, maxVal, step)
				for _, v := range rangeVals {
					values[v] = struct{}{}
				}
			} else if strings.Contains(part, "-") {
				// 范围格式 1-5
				rangeParts := strings.Split(part, "-")
				if len(rangeParts) != 2 {
					return nil, fmt.Errorf("范围格式非法：%s", part)
				}
				start, err := strconv.Atoi(rangeParts[0])
				if err != nil {
					return nil, fmt.Errorf("范围起始值非法：%s（必须是数字）", rangeParts[0])
				}
				end, err := strconv.Atoi(rangeParts[1])
				if err != nil {
					return nil, fmt.Errorf("范围结束值非法：%s（必须是数字）", rangeParts[1])
				}
				if start > end {
					return nil, fmt.Errorf("范围非法：%s（起始值大于结束值）", part)
				}
				if start < minVal || end > maxVal {
					return nil, fmt.Errorf("范围超出边界：%s（应在%d-%d之间）", part, minVal, maxVal)
				}
				rangeVals := generateRange(start, end, step)
				for _, v := range rangeVals {
					values[v] = struct{}{}
				}
			} else {
				// 单个数字
				num, err := strconv.Atoi(part)
				if err != nil {
					return nil, fmt.Errorf("字段值非法：%s（必须是数字）", part)
				}
				if num < minVal || num > maxVal {
					return nil, fmt.Errorf("字段值超出范围：%d（应在%d-%d之间）", num, minVal, maxVal)
				}
				values[num] = struct{}{}
			}
		}

		// 转换为有序切片
		result := make([]int, 0, len(values))
		for v := range values {
			result = append(result, v)
		}
		sort.Ints(result)

		return result, nil
	}

	// 解析各字段（分：0-59，时：0-23，日：1-31，月：1-12，周：0-6）
	minute, err := parseField(minuteStr, 0, 59)
	if err != nil {
		return false, err.Error(), nil
	}
	hour, err := parseField(hourStr, 0, 23)
	if err != nil {
		return false, err.Error(), nil
	}
	day, err := parseField(dayStr, 1, 31)
	if err != nil {
		return false, err.Error(), nil
	}
	month, err := parseField(monthStr, 1, 12)
	if err != nil {
		return false, err.Error(), nil
	}
	weekday, err := parseField(weekdayStr, 0, 6)
	if err != nil {
		return false, err.Error(), nil
	}

	// 语义校验：月份和日期都是具体值时，检查日期在当月是否合法
	// 仅在月份非通配时校验，避免 "每月都可能有31号" 的合法场景误报
	if monthStr != "*" {
		for _, m := range month {
			mMax := getMonthMaxDay(2028, m) // 用闰年，确保2月29日合法
			for _, d := range day {
				if d > mMax {
					return false, fmt.Sprintf("%d月没有%d号（当月最多%d天）", m, d, mMax), nil
				}
			}
		}
	}

	return true, "", &CrontabParsed{
		Minute:  minute,
		Hour:    hour,
		Day:     day,
		Month:   month,
		Weekday: weekday,
	}
}

// validateCrontabFormat 校验crontab格式合法性
func validateCrontabFormat(crontabStr string) (bool, string) {
	// 分割字段并检查数量
	fields := filterEmpty(strings.Fields(crontabStr))
	if len(fields) != 5 {
		return false, fmt.Sprintf("字段数量错误，需为5个（分 时 日 月 周），实际为%d个", len(fields))
	}

	// 字段规则：名称、最小值、最大值、字段值
	type fieldRule struct {
		name  string
		min   int
		max   int
		value string
	}
	rules := []fieldRule{
		{"分钟", 0, 59, fields[0]},
		{"小时", 0, 23, fields[1]},
		{"日期", 1, 31, fields[2]},
		{"月份", 1, 12, fields[3]},
		{"星期", 0, 6, fields[4]},
	}

	for _, rule := range rules {
		// 1. 检查基础格式
		if !fieldPattern.MatchString(rule.value) {
			return false, fmt.Sprintf("%s字段格式非法：%s，支持格式：*、数字、1-5、*/5、1-10/2、1,3,5", rule.name, rule.value)
		}

		// 2. 提取所有数字并检查范围
		numParts := numSplitPattern.Split(strings.ReplaceAll(rule.value, "*", ""), -1)
		numParts = filterEmpty(numParts)

		for _, part := range numParts {
			num, err := strconv.Atoi(part)
			if err != nil {
				return false, fmt.Sprintf("%s字段包含非数字字符：%s", rule.name, part)
			}
			if num < rule.min || num > rule.max {
				return false, fmt.Sprintf("%s字段值超出范围：%d（应在%d-%d之间）", rule.name, num, rule.min, rule.max)
			}
		}

	}

	return true, ""
}





// isDateMatchCrontab 校验日期是否匹配crontab的月/日/周规则
func isDateMatchCrontab(checkDate time.Time, parsed *CrontabParsed) bool {
	// 提取日期属性
	checkMonth := int(checkDate.Month())
	checkDay := checkDate.Day()

	// 转换周几：Go的Weekday() 0=周日,1=周一...6=周六（正好匹配crontab格式）
	checkWeekday := int(checkDate.Weekday())

	// 校验月份
	if !intInSlice(checkMonth, parsed.Month) {
		return false
	}

	// 校验日期（考虑当月最大天数）
	maxDay := getMonthMaxDay(checkDate.Year(), checkMonth)
	validDays := make([]int, 0)
	for _, d := range parsed.Day {
		if d >= 1 && d <= maxDay {
			validDays = append(validDays, d)
		}
	}
	if !intInSlice(checkDay, validDays) {
		return false
	}

	// 校验周几（如果weekday不是全量匹配）
	weekdayAll := []int{0, 1, 2, 3, 4, 5, 6}
	if !sliceEqual(parsed.Weekday, weekdayAll) && !intInSlice(checkWeekday, parsed.Weekday) {
		return false
	}

	return true
}

// generateDailyTimes 生成指定日期内所有符合时/分/秒的时间点（秒默认0）
func generateDailyTimes(targetDate time.Time, parsed *CrontabParsed) []time.Time {
	scheduleTimes := make([]time.Time, 0)

	// 各时间字段（秒默认[0]）
	seconds := []int{0}
	minutes := parsed.Minute
	hours := parsed.Hour

	// 生成所有组合
	for _, hour := range hours {
		for _, minute := range minutes {
			for _, second := range seconds {
				// 构建时间点（忽略错误，因为字段已校验）
				runTime := time.Date(
					targetDate.Year(),
					targetDate.Month(),
					targetDate.Day(),
					hour,
					minute,
					second,
					0,
					targetDate.Location(),
				)
				scheduleTimes = append(scheduleTimes, runTime)
			}
		}
	}

	return scheduleTimes
}

// ---------------- 工具函数 ----------------

// filterEmpty 过滤切片中的空字符串
func filterEmpty(slice []string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if strings.TrimSpace(s) != "" {
			result = append(result, s)
		}
	}
	return result
}

// generateRange 生成[start, end]范围内步长为step的整数切片
func generateRange(start, end, step int) []int {
	result := make([]int, 0)
	for i := start; i <= end; i += step {
		result = append(result, i)
	}
	return result
}

// intInSlice 检查整数是否在切片中
func intInSlice(num int, slice []int) bool {
	for _, v := range slice {
		if v == num {
			return true
		}
	}
	return false
}

// getMonthMaxDay 获取指定年月的最大天数
func getMonthMaxDay(year, month int) int {
	// time.Date 自动处理月份溢出：month=12 时 month+1=13 等价于下一年1月
	// 因此 time.Date(year, 13, 0, ...) 正确返回 year年12月的最后一天
	lastDay := time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC)
	return lastDay.Day()
}

// sliceEqual 检查两个整数切片是否相等（不考虑顺序）
func sliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	setA := make(map[int]struct{})
	for _, v := range a {
		setA[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := setA[v]; !ok {
			return false
		}
	}
	return true
}

// GetAllTaskScheduleFullNext10 获取从当前时间起的下10个任务执行时间点
// crontabStr: 标准5位crontab表达式
// 返回：是否成功、错误信息、时间点列表
func GetAllTaskScheduleFullNext10(crontabStr string) (bool, string, []time.Time) {
	// 先解析校验
	success, msg, parsed := ParseCrontab(crontabStr)
	if !success {
		return false, msg, nil
	}

	results := make([]time.Time, 0, 10)
	now := time.Now()
	// 从当前时间开始，逐天往后扫描，直到凑满10个时间点或超过1年
	checkDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	maxDate := checkDate.AddDate(1, 0, 0) // 最多扫描1年

	for checkDate.Before(maxDate) && len(results) < 10 {
		// 检查当天是否匹配 crontab 的月/日/周规则
		if isDateMatchCrontab(checkDate, parsed) {
			// 生成当天所有时间点
			dailyTimes := generateDailyTimes(checkDate, parsed)
			for _, t := range dailyTimes {
				// 只取当前时间之后的
				if t.After(now) {
					results = append(results, t)
					if len(results) >= 10 {
						break
					}
				}
			}
		}
		checkDate = checkDate.AddDate(0, 0, 1) // 下一天
	}

	return true, "", results
}
