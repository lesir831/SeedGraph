import { createContext, useContext } from 'react'
import type { LoginInput } from '../api/types'

export interface AuthContextValue {
  authenticated: boolean
  checking: boolean
  username?: string
  login: (input: LoginInput) => Promise<void>
  logout: () => Promise<void>
}

export const AuthContext = createContext<AuthContextValue | null>(null)

export const useAuth = (): AuthContextValue => {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth 必须在 AuthProvider 中使用')
  return value
}
