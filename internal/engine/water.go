package engine

// 预先处理每个点的场向量  只计算地势的影响 启动只执行1次
// 旧版这里用 range 值拷贝，结果从未写回，相当于什么都没做；FixFieldInit 打开后才真正计算
func (s *Sim) assignAllVectors(ring int) {
	if !s.p.Compat.FixFieldInit {
		return
	}
	for i := range s.w.Data {
		if s.w.Data[i].assignVector(&s.w, &s.m, ring) {
			s.debugf("field no power, try flow to margin. x,y:%d,%d", i%s.w.Width, i/s.w.Width)
		}
	}
}

// 假设每个点都有一个场，计算出这个场的方向。返回 true 表示周围平坦，没有力
func (dot *WaterDot) assignVector(w *WaterMap, m *Topomap, ring int) (noPower bool) {
	var xPower, yPower int
	curX, curY := int(dot.X), int(dot.Y)
	idx := curX + curY*w.Width
	curLv := m.Data[idx] + w.Data[idx].C - int16(calcBit(w.Data[idx].Q))

	m.FindNeiDots(curX, curY, ring, func(nx, ny, distSq int) {
		nIdx := nx + ny*w.Width
		neiLv := m.Data[nIdx] + w.Data[nIdx].C - int16(calcBit(w.Data[nIdx].Q))
		if neiLv < curLv {
			// 优先考虑邻居低处的吸引力
			xPower += (nx - curX) * int(curLv-neiLv) * int(curLv-neiLv) * 10
			yPower += (ny - curY) * int(curLv-neiLv) * int(curLv-neiLv) * 10
		} else if neiLv > curLv {
			// 邻居高的时候，提供一定的推力
			xPower += (curX - nx) * int(neiLv-curLv) * int(neiLv-curLv) * 2
			yPower += (curY - ny) * int(neiLv-curLv) * int(neiLv-curLv) * 2
		}
	})

	// 平坦时候朝地图周围走
	if xPower == 0 && yPower == 0 {
		xPower, yPower = (calcBit(curX-(m.Width/2))+4)/4, (calcBit(curY-(m.Length/2))+4)/4
		noPower = true
	}

	dot.XPower, dot.YPower = float32(xPower), float32(yPower)
	for dot.XPower*dot.XPower+dot.YPower*dot.YPower > MinDistToReduce {
		dot.XPower, dot.YPower = dot.XPower*0.95, dot.YPower*0.95
	}
	return
}

// 让积水向低处交换，并顺带侵蚀高处邻居
// 会让水聚集在低处；会造成碎石边缘效果
func (s *Sim) collectWater() {
	m, w := &s.m, &s.w
	buf := s.cwBuf[:0]
	for idx := 0; idx < w.Width*w.Length; idx++ {
		if m.Data[idx] < SeaLevel {
			continue
		}
		m.FindNeiDots(int(w.Data[idx].X), int(w.Data[idx].Y), 2, func(nx, ny, distQ int) {
			nIdx := nx + ny*m.Width
			catchDiff := (m.Data[nIdx] + w.Data[nIdx].C) - (m.Data[idx] + w.Data[idx].C)
			// 如果发现邻居水多，则和邻居交换以持平 q>0 很重要，否则会无差别荡平
			if catchDiff <= 1 || w.Data[nIdx].Q <= 0 {
				return
			}
			s.debugf("collect w.exchange: %d -> %d c: %d->%d distQ:%d diff:%d evtId:%d,%d", idx, nIdx, w.Data[nIdx].C, w.Data[idx].C, distQ, catchDiff, m.EvtIdx, w.EvtIdx)
			outflow := catchDiff / 2 // catchDiff/3 会导致水源不足
			if m.Data[idx] == SeaLevel {
				outflow = 0
			}
			w.Data[idx].C, w.Data[nIdx].C = w.Data[idx].C+outflow, w.Data[nIdx].C-catchDiff/2
			w.ExchCnt++
			s.markDirty(idx)
			s.markDirty(nIdx)
			if w.EvtIdx&0x3 <= 1 && m.Data[idx] > SeaLevel && m.Data[nIdx] > m.Data[idx] {
				m.Data[nIdx]--
				s.erodeCnt[nIdx] = satInc(s.erodeCnt[nIdx])
				w.Data[nIdx].Q++
				if distQ > 2 { // 如果是远距离的，将途中的点也侵蚀 以防止天坑
					middleX := w.Data[idx].X + (w.Data[nIdx].X-w.Data[idx].X)/2
					middleY := w.Data[idx].Y + (w.Data[nIdx].Y-w.Data[idx].Y)/2
					middleIdx := int(middleX) + int(middleY)*m.Width
					if m.Data[middleIdx] > m.Data[idx] {
						buf = append(buf, event{kind: evTopo, a: int32(middleIdx), plus: -1})
					}
				}
			}
		})
	}
	for _, e := range buf {
		s.apply(e)
	}
	s.cwBuf = buf
}
