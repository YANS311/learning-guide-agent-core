# 课程先修关系图谱分析系统

独立的只读课程拓扑分析服务。支持检索课程、查看多级先修、生成可行学习顺序、比较两门课的依赖、检查缺失先修和查看下游影响。它不写入图谱，不做 OBE 支撑统计，也不调用语言模型。

## 本地运行

在本目录使用 Python 3.10 或兼容环境：

```sh
python -m pip install -r requirements.txt
python -m prereq.app --port 8094
```

访问 `http://127.0.0.1:8094/`。API 文档位于 `/docs`，存活和依赖就绪分别位于 `/health/live`、`/health/ready`。默认数据是随代码提供的合成课程，不是任何学校的培养方案。可用环境变量 `PREREQ_SNAPSHOT` 指定同结构 JSON；系统只读该文件。使用原子替换切换文件，进程内最多保留上次成功读取的版本 30 秒。就绪检查使用同一版本缓存，不是实时的数据库在线探针。

先修边从前置课指向目标课。建议先修以虚线标记；在「关系范围」中可单独查看必需边。学习顺序给出拓扑上的可行顺序，不代表实际选课时间表；代表路径不枚举所有路径。

## 接口与验证

`GET /api/v1/courses` 检索课程；`GET /api/v1/prerequisites/{course_id}` 查看先修闭包，追加 `/dependents` 查看反向影响。`POST /api/v1/prerequisites/learning-order`、`/compare`、`/missing` 分析对应视图。各分析响应附带快照摘要；`/api/v1/metrics` 暴露进程内缓存命中、未命中和源读取尝试次数。

```sh
python -m pip install -r requirements-dev.txt
python -m pytest -q -p no:cacheprovider
```

Neo4j 集成只用于独立合成环境，须同时配置 `PREREQ_LAB_BOLT` 和 `PREREQ_LAB_MARKER`。库名固定为 `prereq-phase4-test`，地址固定为 IPv4 环回的非默认端口；无标记或不匹配时不读取课程。应用本身没有 Neo4j 写接口。隔离测试库初始化和浏览器回归步骤见 CI；初始化脚本不用于已有数据库。
