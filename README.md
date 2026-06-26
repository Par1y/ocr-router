# OCR Router

一个具有多Provider支持、质量评估和自动回退功能的OCR路由系统。

## 特性

- **多Provider支持**: NVIDIA Nemotron OCR、通用LLM Vision（StepFun等）、浏览器SSE（dots.mocr）
- **质量评估**: 使用独立LLM对OCR结果进行质量评分
- **自动回退**: 当质量不达标时自动切换到下一个Provider，保留最佳结果
- **尝试记录**: 记录每次OCR尝试的评分，便于对比分析
- **异步任务**: 支持异步OCR任务处理
- **CLI和WebUI**: 提供命令行和Web界面，功能同步

## 快速开始

### 1. 配置

编辑 `config.yaml` 文件，设置API密钥：

```yaml
providers:
  nvidia:
    api_key: "${NVIDIA_API_KEY}"
  llm_vision:
    api_key: "${STEP_API_KEY}"
```

或使用环境变量：

```bash
export NVIDIA_API_KEY="your-nvidia-api-key"
export STEP_API_KEY="your-step-api-key"
```

### 2. 使用CLI

```bash
# 单张图片识别
./bin/ocr-cli recognize ./001.png

# 指定Provider
./bin/ocr-cli recognize ./001.png --provider nvidia

# JSON格式输出
./bin/ocr-cli recognize ./001.png --format json

# 批量识别
./bin/ocr-cli batch ./images/ --workers 3

# 批量识别并保存JSON
./bin/ocr-cli batch ./images/ --workers 3 --save-json

# 查看Provider状态
./bin/ocr-cli providers

# 启动HTTP服务器
./bin/ocr-cli serve --port 8080
```

### 3. 使用WebUI

启动服务器后访问 http://localhost:8080

## API接口

### 同步OCR

```bash
curl -X POST http://localhost:8080/api/ocr/sync \
  -H "Content-Type: application/json" \
  -d '{"image_path": "./001.png"}'
```

### 异步OCR

```bash
curl -X POST http://localhost:8080/api/ocr/async \
  -H "Content-Type: application/json" \
  -d '{"image_path": "./001.png"}'
```

### 查询任务状态

```bash
curl http://localhost:8080/api/tasks/{task-id}
```

### 批量OCR

```bash
curl -X POST http://localhost:8080/api/ocr/batch \
  -H "Content-Type: application/json" \
  -d '{"image_paths": ["./001.png", "./002.png"]}'
```

## 配置说明

### Provider配置

| Provider    | 类型        | 说明                              |
| ----------- | ----------- | --------------------------------- |
| nvidia      | nvidia      | NVIDIA Nemotron OCR v2            |
| llm_vision  | llm_vision  | 通用OpenAI兼容接口（默认StepFun） |
| browser_sse | browser_sse | 浏览器SSE（如dots.mocr）          |

### 回退策略

```yaml
fallback:
  strategy: "sequential" # sequential 或 random
  max_retries: 3
  retry_delay: "2s"
  providers:
    - name: "nvidia"
      priority: 1
      enabled: true
    - name: "llm_vision"
      priority: 2
      enabled: true
    - name: "browser_sse"
      priority: 3
      enabled: true
```

### 质量评估

```yaml
evaluator:
  enabled: true
  endpoint: "https://api.stepfun.com/step_plan/v1/chat/completions"
  api_key: "${STEP_API_KEY}"
  model: "step-router-v1"
  threshold: 0.7
  max_retries: 3
  retry_delay: "1s"
  timeout: "30s"
```

## 输出格式

### JSON输出（含尝试记录）

```json
{
  "provider": "llm_vision",
  "fallback": true,
  "original": "nvidia",
  "text": "识别的文本...",
  "evaluation": {
    "score": 0.85,
    "reason": "识别准确，少量格式问题",
    "pass": true,
    "details": {
      "accuracy": 0.8,
      "completeness": 0.9,
      "readability": 0.85
    }
  },
  "attempts": [
    {"provider": "nvidia", "score": 0.45, "passed": false, "text_length": 50},
    {"provider": "llm_vision", "score": 0.85, "passed": true, "text_length": 150}
  ],
  "timestamp": "2024-01-01T12:00:00Z",
  "duration": "5.2s"
}
```

### CLI文本输出

```
识别的文本内容...

--- 尝试记录 ---
  ✗ nvidia: 0.45
  ✓ llm_vision: 0.85

[最终评分: 0.85]
```

### 批量输出

```
results/
├── 001.txt   # 文本格式
├── 001.json  # JSON格式（使用 --save-json）
├── 002.txt
├── 002.json
```

## 开发

### 构建

```bash
go build -o bin/ocr-cli ./cmd/ocr-cli
```

### 项目结构

```
ocr-router/
├── cmd/
│   └── ocr-cli/           # CLI入口
│       ├── main.go
│       ├── recognize.go   # 单张识别
│       ├── batch.go       # 批量识别
│       ├── serve.go       # HTTP服务器
│       ├── task.go        # 任务管理
│       ├── providers.go   # Provider列表
│       └── health.go      # 健康检查
├── internal/
│   ├── config/            # 配置管理
│   ├── ocr/               # OCR核心
│   │   ├── provider.go    # Provider接口
│   │   ├── nvidia.go      # NVIDIA实现
│   │   ├── llm_vision.go  # LLM Vision实现
│   │   ├── browser_sse.go # 浏览器SSE实现
│   │   ├── evaluator.go   # 质量评估器
│   │   └── fallback.go    # 回退引擎
│   ├── task/              # 异步任务系统
│   ├── storage/           # 存储层
│   ├── handler/           # HTTP处理器
│   └── logger/            # 日志系统
├── templates/             # WebUI模板
├── static/                # 静态资源
├── config.yaml            # 配置文件
└── README.md
```

## 许可证

MIT
