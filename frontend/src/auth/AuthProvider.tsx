import { useCallback, useEffect, useMemo, useState, type PropsWithChildren } from 'react'
import { api, authStorage } from '../api/client'
import type { LoginInput } from '../api/types'
import { AuthContext } from './auth-context'

export function AuthProvider({ children }: PropsWithChildren) {
  const [authenticated, setAuthenticated] = useState(authStorage.isAuthenticated())
  const [checking, setChecking] = useState(true)
  const [username, setUsername] = useState<string>()

  const clearSession = useCallback(() => {
    authStorage.clear()
    setAuthenticated(false)
    setUsername(undefined)
  }, [])

  useEffect(() => {
    const validate = async () => {
      try {
        const session = await api.getSession()
        if (session.authenticated === false) {
          clearSession()
          return
        }
        authStorage.save(session)
        setAuthenticated(true)
        setUsername(session.username ?? session.user?.username)
      } catch {
        clearSession()
      } finally {
        setChecking(false)
      }
    }

    void validate()
  }, [clearSession])

  useEffect(() => {
    window.addEventListener('seedgraph:unauthorized', clearSession)
    return () => window.removeEventListener('seedgraph:unauthorized', clearSession)
  }, [clearSession])

  const login = useCallback(async (input: LoginInput) => {
    const session = await api.login(input)
    authStorage.save(session)
    setAuthenticated(true)
    setUsername(session.username ?? session.user?.username ?? input.username)
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } finally {
      clearSession()
    }
  }, [clearSession])

  const value = useMemo(
    () => ({ authenticated, checking, username, login, logout }),
    [authenticated, checking, login, logout, username],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
