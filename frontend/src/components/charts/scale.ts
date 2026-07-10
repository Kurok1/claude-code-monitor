/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.5.0
 */

// niceCeil rounds a chart max up to a "nice" tick ceiling (1/2/5 × 10^n)。
// 独立成文件是为满足 react-refresh/only-export-components:图表组件文件
// 不能再导出非组件函数。
export function niceCeil(v: number): number {
  const pow = Math.pow(10, Math.floor(Math.log10(v)));
  const m = v / pow;
  let nice: number;
  if (m <= 1) nice = 1;
  else if (m <= 2) nice = 2;
  else if (m <= 5) nice = 5;
  else nice = 10;
  return nice * pow;
}
