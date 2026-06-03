export interface BalanceSplitInput {
  balance?: number | null
  paid_balance?: number | null
  gift_balance?: number | null
  total_recharged?: number | null
  total_gifted?: number | null
}

export interface BalanceSplit {
  paid: number
  gift: number
}

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max)

export function getBalanceSplit(input: BalanceSplitInput | null | undefined): BalanceSplit {
  const balance = Math.max(0, Number(input?.balance || 0))
  if (input?.paid_balance !== undefined || input?.gift_balance !== undefined) {
    const paid = clamp(Number(input?.paid_balance || 0), 0, balance)
    const gift = clamp(Number(input?.gift_balance || 0), 0, balance - paid)
    return { paid, gift }
  }
  const totalPaid = Math.max(0, Number(input?.total_recharged || 0))
  const totalGift = Math.max(0, Number(input?.total_gifted || 0))
  const spent = Math.max(0, totalPaid + totalGift - balance)
  const gift = clamp(totalGift - spent, 0, balance)
  return {
    gift,
    paid: Math.max(0, balance - gift)
  }
}

export function formatBalanceAmount(value: number): string {
  if (!Number.isFinite(value) || Math.abs(value) < 1e-10) return '0.00'
  const formatted = value.toFixed(8).replace(/\.?0+$/, '')
  const parts = formatted.split('.')
  if (parts.length === 1) return `${formatted}.00`
  if (parts[1].length === 1) return `${formatted}0`
  return formatted
}
