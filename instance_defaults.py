"""Nonpersonal gameplay defaults matching the playback-confirmed instance.
No admin credentials, provider IDs, accounts or recorded server identity.
"""
STARTUP = '''vars.playerLimit 32
vars.idleTimeout 0
vars.hardCore false
vars.3dSpotting true
vars.crossHair true
vars.miniMap true
vars.miniMapSpotting true
vars.killCam true
vars.teamBalance true
vars.thirdPersonVehicleCameras true
vars.rankLimit -1
vars.profanityFilter false
vars.teamKillCountForKick 6
vars.teamKillValueForKick 4100
vars.teamKillValueIncrease 1000
vars.teamKillValueDecreasePerSecond 50
vars.gamePassword ""
vars.textChatModerationMode free
vars.textChatSpamTriggerCount 5
vars.textChatSpamDetectionTime 10
vars.textChatSpamCoolDownTime 60
levelVars.set all tickets 100
levelVars.set Levels/MP_006CQ tickets 100
levelVars.set MP_006CQ tickets 100
levelVars.set levels/mp_006cq tickets 100
levelVars.set mp_006cq tickets 100
levelVars.set all ticketBleedSpeed 100
levelVars.set all vehicleSpawnRate 100
levelVars.set all startDelay 4
levelVars.set all respawnDelay 12
vars.friendlyFire false
'''
