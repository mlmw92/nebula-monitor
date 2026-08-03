// geoData.js — 地图 GeoJSON 加载注册与名称匹配（离线内置，无需 CDN）。
import { echarts } from './echarts'

// 中国省份名归一：去行政后缀，对齐后端返回的省简称（"广东"、"北京"）。
const provinceSuffixes = [
  '壮族自治区',
  '维吾尔自治区',
  '回族自治区',
  '特别行政区',
  '自治区',
  '省',
  '市',
]

export function normChinaName(name) {
  for (const suf of provinceSuffixes) {
    if (name.endsWith(suf)) return name.slice(0, -suf.length)
  }
  return name
}

// 世界地图（echarts 官方 world.json，英文国家名）与 ip2region 英文国家名的差异修正。
// ip2region 名 → world.json 名；未收录的 ip2region 名通常与 world.json 一致。
export const worldAlias = {
  'United States': 'United States of America',
  'Czech Republic': 'Czech Rep.',
  'Bosnia and Herzegovina': 'Bosnia and Herz.',
  'Antigua and Barbuda': 'Antigua and Barb.',
  'South Sudan': 'S. Sudan',
  'Central African Republic': 'Central African Rep.',
  'Equatorial Guinea': 'Eq. Guinea',
  'Dominican Republic': 'Dominican Rep.',
  'Solomon Islands': 'Solomon Is.',
  'Falkland Islands': 'Falkland Is.',
  'DR Congo': 'Dem. Rep. Congo',
  'Congo': 'Congo',
  'Cote d\'Ivoire': "Côte d'Ivoire",
  'North Macedonia': 'Macedonia',
  'Swaziland': 'Swaziland',
  'Tanzania': 'Tanzania',
  'Laos': 'Laos',
  'Brunei': 'Brunei',
  'Vietnam': 'Vietnam',
  'Cambodia': 'Cambodia',
  'Myanmar': 'Myanmar',
  'Kyrgyzstan': 'Kyrgyzstan',
}

// mapName 返回某点在地图 GeoJSON 中的匹配名：中国地图用省简称，世界地图用英文国家名。
export function mapName(scope, p) {
  if (scope === 'world') {
    return worldAlias[p.countryEn] || p.countryEn || p.name
  }
  return p.name
}

let mapsReady = null

// registerMaps 异步加载并注册中国/世界地图（仅首次调用生效，返回 Promise）。
export function registerMaps() {
  if (!mapsReady) {
    mapsReady = Promise.all([
      import('../assets/geo/china.json').then((m) => {
        const geo = m.default || m
        geo.features.forEach((f) => {
          if (f.properties && f.properties.name) {
            f.properties.name = normChinaName(f.properties.name)
          }
        })
        echarts.registerMap('china', geo)
      }),
      import('../assets/geo/world.json').then((m) => {
        const geo = m.default || m
        echarts.registerMap('world', geo)
      }),
    ])
  }
  return mapsReady
}
