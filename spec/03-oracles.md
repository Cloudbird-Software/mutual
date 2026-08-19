# 03-Oracles：评测 Oracle 定义

> Oracle = 判定"推荐对不对"的确定性标准。
> 开发期全部复用 benchmark / 数据集，不找真实世界 oracle。

## 1. 推荐质量 Oracle

### 1.1 HR@K (Hit Rate at K)

**定义**：在 top-K 推荐列表中，ground-truth 是否出现。

**公式**：
```
HR@K = (命中数) / (总场景数)
```

**实现**：复用 AgentRecBench `RecommendationEvaluator.calculate_hr_at_n`。

**场景设置**：每题 20 个候选，1 个正样本 + 19 个负样本。

### 1.2 NDCG@5

**定义**：归一化折损累积增益，衡量 ground-truth 在列表中的排名质量。

**公式**（单 ground-truth 时 IDCG=1）：
```
NDCG@5 = (1/log2(rank+1)) if rank ≤ 5 else 0
```

**实现**：同上。

### 1.3 门禁标准

```python
assert evaluation_report.hr_at_3 >= 0.6, f"HR@3={hr_at_3} < 0.6"
assert evaluation_report.ndcg_at_5 >= 0.4, f"NDCG@5={ndcg} < 0.4"
```

## 2. 互惠公平 Oracle

### 2.1 Envy-Freeness

**定义**：在匹配结果中，没有任何参与者嫉妒另一个参与者获得的匹配。

**来源**：FairRec `Market.check_envy`。

**输出**：
```python
{
    "left": [(envier_id, envied_id, ...)],   # 左侧嫉妒列表
    "right": [(envier_id, envied_id, ...)]    # 右侧嫉妒列表
}
```

### 2.2 门禁标准

```python
total_envy = evaluation_report.envy_count_left + evaluation_report.envy_count_right
assert total_envy <= 2, f"Envy={total_envy} > 2"
```

## 3. Oracle 数据来源

| Oracle | 数据来源 | 说明 |
|---|---|---|
| HR/NDCG | AgentRecBench 三场景数据 | 经典推荐 / 兴趣变化 / 冷启动 |
| Envy | FairRec `Market.generate_preferences` | 合成双边偏好市场 |

### 3.1 合成市场生成

```python
mkt = Market(num_left=30, num_right=20)
mkt.generate_preferences(pref_seed=0)  # 固定 seed 确保可复现
```

### 3.2 互惠 Bench 场景映射

| AgentRecBench 场景 | 互惠映射 | 构造方式 |
|---|---|---|
| 经典推荐 | 稳定双向偏好 | 合成市场，偏好不随时间变 |
| 兴趣变化 | 一方需求演化 | 合成市场 + 时间步偏好漂移 |
| 冷启动 | 新实体无历史 | 合成市场，新实体只有文本画像无偏好历史 |

## 4. LLM 自我改进反馈注入

| 反馈层级 | 方式 | 触发条件 |
|---|---|---|
| Prompt 校准 | 历史 HR/NDCG 写入 few-shot | 每轮评测后 |
| 权重校准 | 调整 `blend.embed_weight / llm_weight` | HR 下降时 |
| Agent 记忆 | 记录接受/拒绝的匹配 | 用户反馈到达时（v2） |

## 5. 评测即 Spec

评测通过标准写入 spec，CI 门禁强制：

```yaml
# config/default.yaml
evaluation:
  gates:
    hr_at_3_min: 0.6
    ndcg_at_5_min: 0.4
    total_envy_max: 2
```

代码实现改版必须回归通过，否则视为 spec 违约，CI 阻断合并。
