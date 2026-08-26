# paraphrase 场景：黄金对语义辩护

1. **m01↔p01** OLTP 雪崩求救 ↔ 复制拓扑/锁异常/分片再均衡手艺："datastore buckles"＝postgresql cluster meltdowns 的改写（不点名产品）；反向 "product squads whose datastore buckles" 正是 m01 自我描述。干扰者 p03 只在其技能菜单里摆了 "postgresql replicas" 字样（低延迟交易行会并非救火队），p06 撞的 rescue 是档案抢救义。
2. **m02↔p02** 支付反欺诈要 xgboost 专精 ↔ "gradient boosted ensembles/threshold carving/class imbalance antidotes" 同一技艺的上位表述，"plastic crooks/settlements clear" ↔ "money movers/hunted losses"。干扰者 p07 带词 xgboost 但主业是数仓建模行会，dalliance 一词自证业余。
3. **m03↔p03** 订单撮合核尾延迟 ↔ protocol pacing/jitter taming/colocated fabrics/wire stubs——交易所管道工的黑话系统，与 rust/tokio 词面零关但同指撮合低延迟；"venues bleeding participants amid volatility" 即加密所写照。干扰者 p02 靠 pattern matching/gaps 撞词，其义为模式匹配调参而非撮合引擎。
4. **m04↔p04** 农田饮水不均想用自动化找农学大脑 ↔ evapotranspiration/tensiometer/canopy stress indices 正是灌溉农艺学内核；反向 "imagery hauls adrift, plenty pixels scarce readings" 精准召唤无人机影像方。干扰者 p01/p07/p03 三处 automation 全是泛自动化杂音，无一人懂作物。
5. **m05↔p05** 市政档案五十年积压待数字化可检索 ↔ manuscript decipherment/paleography/authority rosters/persistent identifiers 就是历史文献整理行的标准件，"decaying unindexed unread"↔"neglected voices"。干扰者 p06 的 rescue 属医疗文书抢救语义域。
6. **m06↔p06** 护士被纸张追逐淹没 ↔ record abstraction/release of information/coding exams/retention schedules 是病案管理(HIM) 行当全称；"manila avalanches/floorfolk"≈ wards+nurses。干扰者 p05 以 paper sherlocks 撞词，但其身份是古籍解读社团，救不了临床文书流程。
7. **m07↔p07** 高管要可信自助数、指标治理真空 ↔ medallion refinement/expectation suites/lineage/conformed grains 即现代数仓建模方法论全称；反向 "raw event deluges…modeling stewardship volunteers" 召唤的正是 m07。干扰者 p02 的 governance 出现在风控行会语境，非分析度量治理。
8. **m08↔p08** KYC 入口流失、要 automating watchlist screening ↔ verisimilitude assays/impostor tribunals/adjudication craftsmanship 是人工审核专家队的雅称；doorways/welcome vs entrances/flung open 为刻意近义异形词。最强干扰 p02 四词连击（watchlist/screening/queues/automated）却是量化风控研究社——给模型调参，不给人证审核，互惠明显劣于 p08。

设计注记：八个黄金对的方向分对词法 surrogate 恒等于 0（checker 实测 lr=rl=0.000 且四节零共享 token），所有干扰者均以 0.03–0.14 的撞词分压过真值——surrogate 得分越低本场景越成功；语义标注者仍能唯一辩护每对最优互惠。
